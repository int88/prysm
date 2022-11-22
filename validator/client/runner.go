package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	fieldparams "github.com/prysmaticlabs/prysm/config/fieldparams"
	"github.com/prysmaticlabs/prysm/config/params"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/encoding/bytesutil"
	"github.com/prysmaticlabs/prysm/time/slots"
	"github.com/prysmaticlabs/prysm/validator/client/iface"
	"github.com/prysmaticlabs/prysm/validator/keymanager"
	"github.com/prysmaticlabs/prysm/validator/keymanager/remote"
	"go.opencensus.io/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// time to wait before trying to reconnect with beacon node.
var backOffPeriod = 10 * time.Second

// Run the main validator routine. This routine exits if the context is
// canceled.
// 运行main validator routine，这个routin退出，如果context被取消
//
// Order of operations:
// 1 - Initialize validator data
// 2 - Wait for validator activation
// 3 - Wait for the next slot start
// 4 - Update assignments
// 5 - Determine role at current slot
// 6 - Perform assigned role, if any
// 操作顺序：
// 1 - 初始化validator data
// 2 - 等待validator激活
// 3 - 等待下一个slot启动
// 4 - 更新assignment
// 5 - 决定在当前slot的role
// 6 - 执行assigned role，如果有的话
func run(ctx context.Context, v iface.Validator) {
	cleanup := v.Done
	defer cleanup()

	// 等待激活
	headSlot, err := waitForActivation(ctx, v)
	if err != nil {
		return // Exit if context is canceled.
	}

	connectionErrorChannel := make(chan error, 1)
	// 用于接收blocks
	go v.ReceiveBlocks(ctx, connectionErrorChannel)
	if err := v.UpdateDuties(ctx, headSlot); err != nil {
		handleAssignmentError(err, headSlot)
	}

	accountsChangedChan := make(chan [][fieldparams.BLSPubkeyLength]byte, 1)
	km, err := v.Keymanager()
	if err != nil {
		log.Fatalf("Could not get keymanager: %v", err)
	}
	sub := km.SubscribeAccountChanges(accountsChangedChan)
	// Set properties on the beacon node like the fee recipient for validators that are being used & active.
	// 设置beacon node的properties，就像为validator设置fee recipient，被使用以及激活
	if err := v.PushProposerSettings(ctx, km); err != nil {
		log.Fatalf("Failed to update proposer settings: %v", err) // allow fatal. skipcq
	}
	for {
		_, cancel := context.WithCancel(ctx)
		ctx, span := trace.StartSpan(ctx, "validator.processSlot")

		select {
		case <-ctx.Done():
			log.Info("Context canceled, stopping validator")
			span.End()
			cancel()
			sub.Unsubscribe()
			close(accountsChangedChan)
			return // Exit if context is canceled.
		case blocksError := <-connectionErrorChannel:
			if blocksError != nil {
				// block stream中断
				log.WithError(blocksError).Warn("block stream interrupted")
				go v.ReceiveBlocks(ctx, connectionErrorChannel)
				continue
			}
		case newKeys := <-accountsChangedChan:
			anyActive, err := v.HandleKeyReload(ctx, newKeys)
			if err != nil {
				log.WithError(err).Error("Could not properly handle reloaded keys")
			}
			if !anyActive {
				// 没有找到active keys，等待activation
				log.Info("No active keys found. Waiting for activation...")
				err := v.WaitForActivation(ctx, accountsChangedChan)
				if err != nil {
					// 等不到validator activation
					log.Fatalf("Could not wait for validator activation: %v", err)
				}
			}
		case slot := <-v.NextSlot():
			// 收到下一个slot
			span.AddAttributes(trace.Int64Attribute("slot", int64(slot))) // lint:ignore uintcast -- This conversion is OK for tracing.
			reloadRemoteKeys(ctx, km)
			// 是否所有validator都已经退出了
			allExited, err := v.AllValidatorsAreExited(ctx)
			if err != nil {
				// 如果validator都退出了，不能进行check
				log.WithError(err).Error("Could not check if validators are exited")
			}
			if allExited {
				// 所有validator都退出了，没有更多的工作要做了
				log.Info("All validators are exited, no more work to perform...")
				continue
			}

			deadline := v.SlotDeadline(slot)
			slotCtx, cancel := context.WithDeadline(ctx, deadline)
			log := log.WithField("slot", slot)
			log.WithField("deadline", deadline).Debug("Set deadline for proposals and attestations")

			// Keep trying to update assignments if they are nil or if we are past an
			// epoch transition in the beacon node's state.
			// 试着持续更新assignments，如果他们为nil或者如果我们经过了一个epoch transition
			// 在beacon node的state
			if err := v.UpdateDuties(ctx, slot); err != nil {
				handleAssignmentError(err, slot)
				cancel()
				span.End()
				continue
			}

			if slots.IsEpochStart(slot) {
				go func() {
					//deadline set for next epoch rounded up
					// 为下一个epoch设置deadlone
					if err := v.PushProposerSettings(ctx, km); err != nil {
						log.Warnf("Failed to update proposer settings: %v", err)
					}
				}()
			}

			// Start fetching domain data for the next epoch.
			// 开始为下一个epoch获取domain data
			if slots.IsEpochEnd(slot) {
				go v.UpdateDomainDataCaches(ctx, slot+1)
			}

			var wg sync.WaitGroup

			allRoles, err := v.RolesAt(ctx, slot)
			if err != nil {
				// 不能获取validator roles
				log.WithError(err).Error("Could not get validator roles")
				span.End()
				continue
			}
			// 执行role
			performRoles(slotCtx, allRoles, v, slot, &wg, span)
		}
	}
}

func reloadRemoteKeys(ctx context.Context, km keymanager.IKeymanager) {
	remoteKm, ok := km.(remote.RemoteKeymanager)
	if ok {
		_, err := remoteKm.ReloadPublicKeys(ctx)
		if err != nil {
			log.WithError(err).Error(msgCouldNotFetchKeys)
		}
	}
}

func waitForActivation(ctx context.Context, v iface.Validator) (types.Slot, error) {
	ticker := time.NewTicker(backOffPeriod)
	defer ticker.Stop()

	var headSlot types.Slot
	firstTime := true
	for {
		if !firstTime {
			if ctx.Err() != nil {
				log.Info("Context canceled, stopping validator")
				return headSlot, errors.New("context canceled")
			}
			<-ticker.C
		} else {
			firstTime = false
		}
		// 等待chain启动
		err := v.WaitForChainStart(ctx)
		if isConnectionError(err) {
			log.Warnf("Could not determine if beacon chain started: %v", err)
			continue
		}
		if err != nil {
			log.Fatalf("Could not determine if beacon chain started: %v", err)
		}

		err = v.WaitForKeymanagerInitialization(ctx)
		// 等待key manager初始化完成
		if err != nil {
			// log.Fatalf will prevent defer from being called
			v.Done()
			log.Fatalf("Wallet is not ready: %v", err)
		}

		err = v.WaitForSync(ctx)
		// 等待同步
		if isConnectionError(err) {
			log.Warnf("Could not determine if beacon chain started: %v", err)
			continue
		}
		if err != nil {
			log.Fatalf("Could not determine if beacon node synced: %v", err)
		}
		err = v.WaitForActivation(ctx, nil /* accountsChangedChan */)
		// 等待validator activation
		if isConnectionError(err) {
			log.Warnf("Could not wait for validator activation: %v", err)
			continue
		}
		if err != nil {
			log.Fatalf("Could not wait for validator activation: %v", err)
		}

		headSlot, err = v.CanonicalHeadSlot(ctx)
		if isConnectionError(err) {
			// 获取当前的canonical head slot
			log.Warnf("Could not get current canonical head slot: %v", err)
			continue
		}
		if err != nil {
			log.Fatalf("Could not get current canonical head slot: %v", err)
		}
		err = v.CheckDoppelGanger(ctx)
		if isConnectionError(err) {
			log.Warnf("Could not wait for checking doppelganger: %v", err)
			continue
		}
		if err != nil {
			log.Fatalf("Could not succeed with doppelganger check: %v", err)
		}
		break
	}
	return headSlot, nil
}

func performRoles(slotCtx context.Context, allRoles map[[48]byte][]iface.ValidatorRole, v iface.Validator, slot types.Slot, wg *sync.WaitGroup, span *trace.Span) {
	for pubKey, roles := range allRoles {
		wg.Add(len(roles))
		for _, role := range roles {
			// 遍历各个roles
			go func(role iface.ValidatorRole, pubKey [fieldparams.BLSPubkeyLength]byte) {
				defer wg.Done()
				switch role {
				case iface.RoleAttester:
					// 执行attester的任务
					v.SubmitAttestation(slotCtx, slot, pubKey)
				case iface.RoleProposer:
					// 提交block
					v.ProposeBlock(slotCtx, slot, pubKey)
				case iface.RoleAggregator:
					// 提交aggregated以及proof
					v.SubmitAggregateAndProof(slotCtx, slot, pubKey)
				case iface.RoleSyncCommittee:
					// 提交sync committee message
					v.SubmitSyncCommitteeMessage(slotCtx, slot, pubKey)
				case iface.RoleSyncCommitteeAggregator:
					// 提交signed contribution以及proof
					v.SubmitSignedContributionAndProof(slotCtx, slot, pubKey)
				case iface.RoleUnknown:
					log.WithField("pubKey", fmt.Sprintf("%#x", bytesutil.Trunc(pubKey[:]))).Trace("No active roles, doing nothing")
				default:
					log.Warnf("Unhandled role %v", role)
				}
			}(role, pubKey)
		}
	}

	// Wait for all processes to complete, then report span complete.
	// 等待所有的处理完成，之后报告span complete
	go func() {
		wg.Wait()
		defer span.End()
		defer func() {
			if err := recover(); err != nil { // catch any panic in logging
				log.WithField("err", err).
					Error("Panic occurred when logging validator report. This" +
						" should never happen! Please file a report at github.com/prysmaticlabs/prysm/issues/new")
			}
		}()
		// Log this client performance in the previous epoch
		// 日志这个client在之前一个epoch的performance
		v.LogAttestationsSubmitted()
		v.LogSyncCommitteeMessagesSubmitted()
		if err := v.LogValidatorGainsAndLosses(slotCtx, slot); err != nil {
			// 不能报告validator的rewards/penalties
			log.WithError(err).Error("Could not report validator's rewards/penalties")
		}
		if err := v.LogNextDutyTimeLeft(slot); err != nil {
			// 不能报告下一次的count down
			log.WithError(err).Error("Could not report next count down")
		}
	}()
}

func isConnectionError(err error) bool {
	return err != nil && errors.Is(err, iface.ErrConnectionIssue)
}

func handleAssignmentError(err error, slot types.Slot) {
	if errCode, ok := status.FromError(err); ok && errCode.Code() == codes.NotFound {
		log.WithField(
			"epoch", slot/params.BeaconConfig().SlotsPerEpoch,
			// 说明validator没有赋予给这个ecoch
		).Warn("Validator not yet assigned to epoch")
	} else {
		// 更新assignment失败
		log.WithField("error", err).Error("Failed to update assignments")
	}
}
