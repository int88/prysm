package stategen

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/altair"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/capella"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/execution"
	prysmtime "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db/filters"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/monitoring/tracing"
	"github.com/prysmaticlabs/prysm/v3/runtime/version"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

// ReplayBlocks replays the input blocks on the input state until the target slot is reached.
// ReplayBlocks重放收入的blocks，针对input state，直到到达target slot
//
// WARNING Blocks passed to the function must be in decreasing slots order.
func (_ *State) replayBlocks(
	ctx context.Context,
	state state.BeaconState,
	signed []interfaces.ReadOnlySignedBeaconBlock,
	targetSlot primitives.Slot,
) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stateGen.replayBlocks")
	defer span.End()
	var err error

	start := time.Now()
	log = log.WithFields(logrus.Fields{
		"startSlot": state.Slot(),
		"endSlot":   targetSlot,
		"diff":      targetSlot - state.Slot(),
	})
	log.Debug("Replaying state")
	// The input block list is sorted in decreasing slots order.
	// 输入的block list按照降slots的顺序排序
	if len(signed) > 0 {
		for i := len(signed) - 1; i >= 0; i-- {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if state.Slot() >= targetSlot {
				// 大于等于target slots就跳出
				break
			}
			// A node shouldn't process the block if the block slot is lower than the state slot.
			// 一个node不能处理block，如果block slot比state slot哟啊小
			if state.Slot() >= signed[i].Block().Slot() {
				continue
			}
			// 执行state transition
			state, err = executeStateTransitionStateGen(ctx, state, signed[i])
			if err != nil {
				return nil, err
			}
		}
	}

	// If there are skip slots at the end.
	// 如果最后有skip slots
	if targetSlot > state.Slot() {
		state, err = ReplayProcessSlots(ctx, state, targetSlot)
		if err != nil {
			return nil, err
		}
	}

	duration := time.Since(start)
	log.WithFields(logrus.Fields{
		"duration": duration,
	}).Debug("Replayed state")

	return state, nil
}

// loadBlocks loads the blocks between start slot and end slot by recursively fetching from end block root.
// The Blocks are returned in slot-descending order.
// loadBlocks加载start slot和end slot之间的blocks，通过递归地从end block root开始抓取
// blocks以slot下降的顺序返回
func (s *State) loadBlocks(ctx context.Context, startSlot, endSlot primitives.Slot, endBlockRoot [32]byte) ([]interfaces.ReadOnlySignedBeaconBlock, error) {
	// Nothing to load for invalid range.
	if startSlot > endSlot {
		return nil, fmt.Errorf("start slot %d > end slot %d", startSlot, endSlot)
	}
	filter := filters.NewFilter().SetStartSlot(startSlot).SetEndSlot(endSlot)
	// 从beacon db中获取blocks和blockRoots
	blocks, blockRoots, err := s.beaconDB.Blocks(ctx, filter)
	if err != nil {
		return nil, err
	}
	// The retrieved blocks and block roots have to be in the same length given same filter.
	if len(blocks) != len(blockRoots) {
		return nil, errors.New("length of blocks and roots don't match")
	}
	// Return early if there's no block given the input.
	// 尽早返回，如果对于给定的输入没有block
	length := len(blocks)
	if length == 0 {
		return nil, nil
	}

	// The last retrieved block root has to match input end block root.
	// Covers the edge case if there's multiple blocks on the same end slot,
	// the end root may not be the last index in `blockRoots`.
	// 最后获取的block root必须匹配输入的end block root，覆盖边界条件，如果有多个blocks在同一个
	// end slot，end root可能不在`blockRoots`的最后一个索引
	for length >= 3 && blocks[length-1].Block().Slot() == blocks[length-2].Block().Slot() && blockRoots[length-1] != endBlockRoot {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		length--
		if blockRoots[length-2] == endBlockRoot {
			length--
			break
		}
	}

	if blockRoots[length-1] != endBlockRoot {
		return nil, errors.New("end block roots don't match")
	}

	filteredBlocks := []interfaces.ReadOnlySignedBeaconBlock{blocks[length-1]}
	// Starting from second to last index because the last block is already in the filtered block list.
	// 从第二个到最后一个索引，因为最后一个block已经在filtered block list中
	for i := length - 2; i >= 0; i-- {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		b := filteredBlocks[len(filteredBlocks)-1]
		if b.Block().ParentRoot() != blockRoots[i] {
			continue
		}
		filteredBlocks = append(filteredBlocks, blocks[i])
	}

	return filteredBlocks, nil
}

// executeStateTransitionStateGen applies state transition on input historical state and block for state gen usages.
// There's no signature verification involved given state gen only works with stored block and state in DB.
// If the objects are already in stored in DB, one can omit redundant signature checks and ssz hashing calculations.
// executeStateTransitionStateGen对于输入的historical state以及block应用state transition，用于state gen
// 没有signature verification，因此只对DB中存储的block以及state有效
// 如果对象已经存储在DB中，则可以忽略冗余的signature checks以及ssz hashing
//
// WARNING: This method should not be used on an unverified new block.
func executeStateTransitionStateGen(
	ctx context.Context,
	state state.BeaconState,
	signed interfaces.ReadOnlySignedBeaconBlock,
) (state.BeaconState, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := blocks.BeaconBlockIsNil(signed); err != nil {
		return nil, err
	}
	ctx, span := trace.StartSpan(ctx, "stategen.executeStateTransitionStateGen")
	defer span.End()
	var err error

	// Execute per slots transition.
	// Given this is for state gen, a node uses the version of process slots without skip slots cache.
	// 执行每个slots的transition
	// 对于给定的state gen，一个节点使用process slots，而不用跳过slot cache
	state, err = ReplayProcessSlots(ctx, state, signed.Block().Slot())
	if err != nil {
		return nil, errors.Wrap(err, "could not process slot")
	}

	// Execute per block transition.
	// 执行每个block transition
	// Given this is for state gen, a node only cares about the post state without proposer
	// and randao signature verifications.
	// 对于给定的state gen，一个节点只关心post state，而不是proposer以及randao signature的校验
	state, err = transition.ProcessBlockForStateRoot(ctx, state, signed)
	if err != nil {
		return nil, errors.Wrap(err, "could not process block")
	}
	return state, nil
}

// ReplayProcessSlots to process old slots for state gen usages.
// ReplayProcessSlots用于处理old slots，用于state gen
// There's no skip slot cache involved given state gen only works with already stored block and state in DB.
// 没有包含skip slot cache，给定state gen，只作用于已经存在在DB中的block和state
//
// WARNING: This method should not be used for future slot.
func ReplayProcessSlots(ctx context.Context, state state.BeaconState, slot primitives.Slot) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stategen.ReplayProcessSlots")
	defer span.End()
	if state == nil || state.IsNil() {
		return nil, errUnknownState
	}

	if state.Slot() > slot {
		err := fmt.Errorf("expected state.slot %d <= slot %d", state.Slot(), slot)
		return nil, err
	}

	if state.Slot() == slot {
		return state, nil
	}

	var err error
	for state.Slot() < slot {
		state, err = transition.ProcessSlot(ctx, state)
		if err != nil {
			return nil, errors.Wrap(err, "could not process slot")
		}
		if prysmtime.CanProcessEpoch(state) {
			// 处理epoch
			switch state.Version() {
			case version.Phase0:
				state, err = transition.ProcessEpochPrecompute(ctx, state)
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
				return nil, fmt.Errorf("unsupported beacon state version: %s", version.String(state.Version()))
			}
		}
		if err := state.SetSlot(state.Slot() + 1); err != nil {
			tracing.AnnotateError(span, err)
			return nil, errors.Wrap(err, "failed to increment state slot")
		}

		if prysmtime.CanUpgradeToAltair(state.Slot()) {
			state, err = altair.UpgradeToAltair(ctx, state)
			if err != nil {
				tracing.AnnotateError(span, err)
				return nil, err
			}
		}

		if prysmtime.CanUpgradeToBellatrix(state.Slot()) {
			state, err = execution.UpgradeToBellatrix(state)
			if err != nil {
				tracing.AnnotateError(span, err)
				return nil, err
			}
		}

		if prysmtime.CanUpgradeToCapella(state.Slot()) {
			state, err = capella.UpgradeToCapella(state)
			if err != nil {
				tracing.AnnotateError(span, err)
				return nil, err
			}
		}
	}

	return state, nil
}

// Given the start slot and the end slot, this returns the finalized beacon blocks in between.
// Since hot states don't have finalized blocks, this should ONLY be used for replaying cold state.
func (s *State) loadFinalizedBlocks(ctx context.Context, startSlot, endSlot primitives.Slot) ([]interfaces.ReadOnlySignedBeaconBlock, error) {
	f := filters.NewFilter().SetStartSlot(startSlot).SetEndSlot(endSlot)
	bs, bRoots, err := s.beaconDB.Blocks(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(bs) != len(bRoots) {
		return nil, errors.New("length of blocks and roots don't match")
	}
	fbs := make([]interfaces.ReadOnlySignedBeaconBlock, 0, len(bs))
	for i := len(bs) - 1; i >= 0; i-- {
		if s.beaconDB.IsFinalizedBlock(ctx, bRoots[i]) {
			fbs = append(fbs, bs[i])
		}
	}
	return fbs, nil
}
