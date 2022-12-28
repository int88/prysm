// Package transition implements the whole state transition
// function which consists of per slot, per-epoch transitions.
// It also bootstraps the genesis beacon state for slot 0.
// transition实现了整个的state transition功能，包括每个slot，每个epoch的transitions
// 它同时生成genesis beacon state，对于slot 0
package transition

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/cache"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/altair"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/capella"
	e "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/epoch"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/epoch/precompute"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/execution"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/math"
	"github.com/prysmaticlabs/prysm/v3/monitoring/tracing"
	"github.com/prysmaticlabs/prysm/v3/runtime/version"
	"go.opencensus.io/trace"
)

// ExecuteStateTransition defines the procedure for a state transition function.
// ExecuteStateTransition定义了状态转换的函数
//
// Note: This method differs from the spec pseudocode as it uses a batch signature verification.
// See: ExecuteStateTransitionNoVerifyAnySig
//
// Spec pseudocode definition:
//
//					def state_transition(state: BeaconState, signed_block: SignedBeaconBlock, validate_result: bool=True) -> None:
//					  block = signed_block.message
//					  # Process slots (including those with no blocks) since block
//				   # 处理slots
//					  process_slots(state, block.slot)
//					  # Verify signature
//			       # 校验signature
//					  if validate_result:
//					      assert verify_block_signature(state, signed_block)
//					  # Process block
//		           # 处理block
//					  process_block(state, block)
//					  # Verify state root
//	               # 校验state root
//					  if validate_result:
//					      assert block.state_root == hash_tree_root(state)
func ExecuteStateTransition(
	ctx context.Context,
	state state.BeaconState,
	signed interfaces.SignedBeaconBlock,
) (state.BeaconState, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := blocks.BeaconBlockIsNil(signed); err != nil {
		return nil, err
	}

	ctx, span := trace.StartSpan(ctx, "core.state.ExecuteStateTransition")
	defer span.End()
	var err error

	// 获取poststate
	set, postState, err := ExecuteStateTransitionNoVerifyAnySig(ctx, state, signed)
	if err != nil {
		return nil, errors.Wrap(err, "could not execute state transition")
	}
	valid, err := set.Verify()
	if err != nil {
		return nil, errors.Wrap(err, "could not batch verify signature")
	}
	if !valid {
		return nil, errors.New("signature in block failed to verify")
	}

	return postState, nil
}

// ProcessSlot happens every slot and focuses on the slot counter and block roots record updates.
// It happens regardless if there's an incoming block or not.
// ProcessSlot发生在每个slot并且专注在slot counter以及block roots记录的更新，不管有没有incoming block
// 它都会发生
// Spec pseudocode definition:
//
//				def process_slot(state: BeaconState) -> None:
//				  # Cache state root
//			   # 缓存state root
//				  previous_state_root = hash_tree_root(state)
//				  state.state_roots[state.slot % SLOTS_PER_HISTORICAL_ROOT] = previous_state_root
//				  # Cache latest block header state root
//		       # 缓存最新的block header state root
//				  if state.latest_block_header.state_root == Bytes32():
//				      state.latest_block_header.state_root = previous_state_root
//				  # Cache block root
//	           # 缓存block root
//				  previous_block_root = hash_tree_root(state.latest_block_header)
//				  state.block_roots[state.slot % SLOTS_PER_HISTORICAL_ROOT] = previous_block_root
func ProcessSlot(ctx context.Context, state state.BeaconState) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessSlot")
	defer span.End()
	span.AddAttributes(trace.Int64Attribute("slot", int64(state.Slot()))) // lint:ignore uintcast -- This is OK for tracing.

	// 获取之前的state root
	prevStateRoot, err := state.HashTreeRoot(ctx)
	if err != nil {
		return nil, err
	}
	// 更新state root
	if err := state.UpdateStateRootAtIndex(
		uint64(state.Slot()%params.BeaconConfig().SlotsPerHistoricalRoot),
		prevStateRoot,
	); err != nil {
		return nil, err
	}

	zeroHash := params.BeaconConfig().ZeroHash
	// Cache latest block header state root.
	// 缓存最新的block header state root
	header := state.LatestBlockHeader()
	if header.StateRoot == nil || bytes.Equal(header.StateRoot, zeroHash[:]) {
		header.StateRoot = prevStateRoot[:]
		if err := state.SetLatestBlockHeader(header); err != nil {
			return nil, err
		}
	}
	// 获取之前的block root
	prevBlockRoot, err := state.LatestBlockHeader().HashTreeRoot()
	if err != nil {
		tracing.AnnotateError(span, err)
		return nil, errors.Wrap(err, "could not determine prev block root")
	}
	// Cache the block root.
	// 缓存block root
	if err := state.UpdateBlockRootAtIndex(
		uint64(state.Slot()%params.BeaconConfig().SlotsPerHistoricalRoot),
		prevBlockRoot,
	); err != nil {
		return nil, err
	}
	return state, nil
}

// ProcessSlotsUsingNextSlotCache processes slots by using next slot cache for higher efficiency.
// ProcessSlotsUsingNextSlotCache处理slots，使用下一个slot cache，为了更高的效率
func ProcessSlotsUsingNextSlotCache(
	ctx context.Context,
	parentState state.BeaconState,
	parentRoot []byte,
	slot types.Slot) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessSlotsUsingNextSlotCache")
	defer span.End()

	// Check whether the parent state has been advanced by 1 slot in next slot cache.
	// 检查是否parent state已经移动了1个slot，在下一个slot cache
	nextSlotState, err := NextSlotState(ctx, parentRoot)
	if err != nil {
		return nil, err
	}
	cachedStateExists := nextSlotState != nil && !nextSlotState.IsNil()
	// If the next slot state is not nil (i.e. cache hit).
	// We replace next slot state with parent state.
	// 如果next slot cache不是nil（例如，cache命中）
	// 我们将parent state替换为next slot state
	if cachedStateExists {
		parentState = nextSlotState
	}

	// In the event our cached state has advanced our
	// state to the desired slot, we exit early.
	// 如果我们缓存的state已经移动我们的state到了期望的slot，我们尽早退出
	if cachedStateExists && parentState.Slot() == slot {
		return parentState, nil
	}
	// Since next slot cache only advances state by 1 slot,
	// we check if there's more slots that need to process.
	// 因为next slot cache只会将state移动1个slot，我们检查是否有更多的slots需要处理
	parentState, err = ProcessSlots(ctx, parentState, slot)
	if err != nil {
		return nil, errors.Wrap(err, "could not process slots")
	}
	return parentState, nil
}

// ProcessSlotsIfPossible executes ProcessSlots on the input state when target slot is above the state's slot.
// Otherwise, it returns the input state unchanged.
// ProcessSlotsIfPossible在输入的state之上执行ProcessSlots，当target slot超过state的slot时
// 否则，返回input state，不发生变更
func ProcessSlotsIfPossible(ctx context.Context, state state.BeaconState, targetSlot types.Slot) (state.BeaconState, error) {
	if targetSlot > state.Slot() {
		return ProcessSlots(ctx, state, targetSlot)
	}
	return state, nil
}

// ProcessSlots process through skip slots and apply epoch transition when it's needed
// ProcessSlots处理skip slots并且应用epoch transition，当需要的话
//
// Spec pseudocode definition:
//
//			def process_slots(state: BeaconState, slot: Slot) -> None:
//			  assert state.slot < slot
//			  while state.slot < slot:
//	           # 处理slot
//			      process_slot(state)
//			      # Process epoch on the start slot of the next epoch
//		       # 在下一个epoch的start slot处理epoch
//			      if (state.slot + 1) % SLOTS_PER_EPOCH == 0:
//			          process_epoch(state)
//			      state.slot = Slot(state.slot + 1)
func ProcessSlots(ctx context.Context, state state.BeaconState, slot types.Slot) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessSlots")
	defer span.End()
	if state == nil || state.IsNil() {
		return nil, errors.New("nil state")
	}
	span.AddAttributes(trace.Int64Attribute("slots", int64(slot)-int64(state.Slot()))) // lint:ignore uintcast -- This is OK for tracing.

	// The block must have a higher slot than parent state.
	// block必须有比parent state更高的slot
	if state.Slot() >= slot {
		err := fmt.Errorf("expected state.slot %d < slot %d", state.Slot(), slot)
		tracing.AnnotateError(span, err)
		return nil, err
	}

	highestSlot := state.Slot()
	key, err := cacheKey(ctx, state)
	if err != nil {
		return nil, err
	}

	// Restart from cached value, if one exists.
	// 从缓存的值重新开始，如果存在的话
	cachedState, err := SkipSlotCache.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	if cachedState != nil && !cachedState.IsNil() && cachedState.Slot() < slot {
		highestSlot = cachedState.Slot()
		state = cachedState
	}
	if err := SkipSlotCache.MarkInProgress(key); errors.Is(err, cache.ErrAlreadyInProgress) {
		cachedState, err = SkipSlotCache.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if cachedState != nil && !cachedState.IsNil() && cachedState.Slot() < slot {
			highestSlot = cachedState.Slot()
			state = cachedState
		}
	} else if err != nil {
		return nil, err
	}
	defer func() {
		SkipSlotCache.MarkNotInProgress(key)
	}()

	for state.Slot() < slot {
		if ctx.Err() != nil {
			tracing.AnnotateError(span, ctx.Err())
			// Cache last best value.
			if highestSlot < state.Slot() {
				if SkipSlotCache.Put(ctx, key, state); err != nil {
					log.WithError(err).Error("Failed to put skip slot cache value")
				}
			}
			return nil, ctx.Err()
		}
		// 处理slot
		state, err = ProcessSlot(ctx, state)
		if err != nil {
			tracing.AnnotateError(span, err)
			return nil, errors.Wrap(err, "could not process slot")
		}
		// 处理epoch
		if time.CanProcessEpoch(state) {
			switch state.Version() {
			case version.Phase0:
				state, err = ProcessEpochPrecompute(ctx, state)
				if err != nil {
					tracing.AnnotateError(span, err)
					return nil, errors.Wrap(err, "could not process epoch with optimizations")
				}
			case version.Altair, version.Bellatrix, version.Capella:
				state, err = altair.ProcessEpoch(ctx, state)
				if err != nil {
					tracing.AnnotateError(span, err)
					return nil, errors.Wrap(err, "could not process epoch")
				}
			default:
				return nil, errors.New("beacon state should have a version")
			}
		}
		// 自增state slot
		if err := state.SetSlot(state.Slot() + 1); err != nil {
			tracing.AnnotateError(span, err)
			// 自增state slot失败
			return nil, errors.Wrap(err, "failed to increment state slot")
		}

		// 是否可以升级到Altair
		if time.CanUpgradeToAltair(state.Slot()) {
			state, err = altair.UpgradeToAltair(ctx, state)
			if err != nil {
				tracing.AnnotateError(span, err)
				return nil, err
			}
		}

		// 是否可以升级到Bellatrix
		if time.CanUpgradeToBellatrix(state.Slot()) {
			state, err = execution.UpgradeToBellatrix(state)
			if err != nil {
				tracing.AnnotateError(span, err)
				return nil, err
			}
		}

		if time.CanUpgradeToCapella(state.Slot()) {
			state, err = capella.UpgradeToCapella(state)
			if err != nil {
				tracing.AnnotateError(span, err)
				return nil, err
			}
		}
	}

	if highestSlot < state.Slot() {
		SkipSlotCache.Put(ctx, key, state)
	}

	return state, nil
}

// VerifyOperationLengths verifies that block operation lengths are valid.
// VerifyOperationLengths校验block operation的长度是合法的
func VerifyOperationLengths(_ context.Context, state state.BeaconState, b interfaces.SignedBeaconBlock) (state.BeaconState, error) {
	if err := blocks.BeaconBlockIsNil(b); err != nil {
		return nil, err
	}
	body := b.Block().Body()

	// 校验proposer slashings的长度
	if uint64(len(body.ProposerSlashings())) > params.BeaconConfig().MaxProposerSlashings {
		return nil, fmt.Errorf(
			"number of proposer slashings (%d) in block body exceeds allowed threshold of %d",
			len(body.ProposerSlashings()),
			params.BeaconConfig().MaxProposerSlashings,
		)
	}

	// 校验attester slashings的长度
	if uint64(len(body.AttesterSlashings())) > params.BeaconConfig().MaxAttesterSlashings {
		return nil, fmt.Errorf(
			"number of attester slashings (%d) in block body exceeds allowed threshold of %d",
			len(body.AttesterSlashings()),
			params.BeaconConfig().MaxAttesterSlashings,
		)
	}

	// 校验attestations的长度
	if uint64(len(body.Attestations())) > params.BeaconConfig().MaxAttestations {
		return nil, fmt.Errorf(
			"number of attestations (%d) in block body exceeds allowed threshold of %d",
			len(body.Attestations()),
			params.BeaconConfig().MaxAttestations,
		)
	}

	// 校验voluntary exits的长度
	if uint64(len(body.VoluntaryExits())) > params.BeaconConfig().MaxVoluntaryExits {
		return nil, fmt.Errorf(
			"number of voluntary exits (%d) in block body exceeds allowed threshold of %d",
			len(body.VoluntaryExits()),
			params.BeaconConfig().MaxVoluntaryExits,
		)
	}
	eth1Data := state.Eth1Data()
	if eth1Data == nil {
		return nil, errors.New("nil eth1data in state")
	}
	// state中的Eth1 deposit index是合法的
	if state.Eth1DepositIndex() > eth1Data.DepositCount {
		return nil, fmt.Errorf("expected state.deposit_index %d <= eth1data.deposit_count %d", state.Eth1DepositIndex(), eth1Data.DepositCount)
	}
	maxDeposits := math.Min(params.BeaconConfig().MaxDeposits, eth1Data.DepositCount-state.Eth1DepositIndex())
	// Verify outstanding deposits are processed up to max number of deposits
	// 校验outstanding deposits已经处理到最大的deposits的数目
	if uint64(len(body.Deposits())) != maxDeposits {
		return nil, fmt.Errorf("incorrect outstanding deposits in block body, wanted: %d, got: %d",
			maxDeposits, len(body.Deposits()))
	}

	return state, nil
}

// ProcessEpochPrecompute describes the per epoch operations that are performed on the beacon state.
// ProcessEpochPrecompute描述了在beacon state至少执行的，每个epoch operations的操作
// It's optimized by pre computing validator attested info and epoch total/attested balances upfront.
// 它的优化，通过提前计算validator attested info以及epoch total/attestd balance
func ProcessEpochPrecompute(ctx context.Context, state state.BeaconState) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessEpochPrecompute")
	defer span.End()
	span.AddAttributes(trace.Int64Attribute("epoch", int64(time.CurrentEpoch(state)))) // lint:ignore uintcast -- This is OK for tracing.

	if state == nil || state.IsNil() {
		return nil, errors.New("nil state")
	}
	vp, bp, err := precompute.New(ctx, state)
	if err != nil {
		return nil, err
	}
	// 处理attestations
	vp, bp, err = precompute.ProcessAttestations(ctx, state, vp, bp)
	if err != nil {
		return nil, err
	}

	// 处理justification以及finalization
	state, err = precompute.ProcessJustificationAndFinalizationPreCompute(state, bp)
	if err != nil {
		return nil, errors.Wrap(err, "could not process justification")
	}

	// 处理rewards以及penalties
	state, err = precompute.ProcessRewardsAndPenaltiesPrecompute(state, bp, vp, precompute.AttestationsDelta, precompute.ProposersDelta)
	if err != nil {
		return nil, errors.Wrap(err, "could not process rewards and penalties")
	}

	// 处理registry updates
	state, err = e.ProcessRegistryUpdates(ctx, state)
	if err != nil {
		return nil, errors.Wrap(err, "could not process registry updates")
	}

	// 处理slashings
	err = precompute.ProcessSlashingsPrecompute(state, bp)
	if err != nil {
		return nil, err
	}

	// 处理final update
	state, err = e.ProcessFinalUpdates(state)
	if err != nil {
		return nil, errors.Wrap(err, "could not process final updates")
	}
	return state, nil
}
