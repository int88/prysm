package stategen

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

var ErrFutureSlotRequested = errors.New("cannot replay to future slots")
var ErrNoCanonicalBlockForSlot = errors.New("none of the blocks found in the db slot index are canonical")
var ErrNoBlocksBelowSlot = errors.New("no blocks found in db below slot")
var ErrReplayTargetSlotExceeded = errors.New("desired replay slot is less than state's slot")

type retrievalMethod int

const (
	forSlot retrievalMethod = iota
)

// HistoryAccessor describes the minimum set of database methods needed to support the ReplayerBuilder.
// HistoryAccessor描述了数据库方法的最小集，用于支持ReplayerBuilder
type HistoryAccessor interface {
	HighestRootsBelowSlot(ctx context.Context, slot primitives.Slot) (primitives.Slot, [][32]byte, error)
	GenesisBlockRoot(ctx context.Context) ([32]byte, error)
	Block(ctx context.Context, blockRoot [32]byte) (interfaces.ReadOnlySignedBeaconBlock, error)
	StateOrError(ctx context.Context, blockRoot [32]byte) (state.BeaconState, error)
}

// CanonicalChecker determines whether the given block root is canonical.
// In practice this should be satisfied by a type that uses the fork choice store.
// CanonicalChecker决定是否给定的block root是canonical的，实际上，它应该被使用fork choice store
// 的一种类型满足
type CanonicalChecker interface {
	IsCanonical(ctx context.Context, blockRoot [32]byte) (bool, error)
}

// CurrentSlotter provides the current Slot.
// CurrentSlotter提供当前的Slot
type CurrentSlotter interface {
	CurrentSlot() primitives.Slot
}

// Replayer encapsulates database query and replay logic. It can be constructed via a ReplayerBuilder.
// Replayer封装了database query和replay逻辑，它可以通过一个ReplayerBuilder构造
type Replayer interface {
	// ReplayBlocks replays the blocks the Replayer knows about based on Builder params
	ReplayBlocks(ctx context.Context) (state.BeaconState, error)
	// ReplayToSlot invokes ReplayBlocks under the hood,
	// ReplayToSlot底层调用ReplayBlocks
	// but then also runs process_slots to advance the state past the root or slot used in the builder.
	// For example, if you wanted the state to be at the target slot, but only integrating blocks up to
	// slot-1, you could request Builder.ReplayerForSlot(slot-1).ReplayToSlot(slot)
	ReplayToSlot(ctx context.Context, target primitives.Slot) (state.BeaconState, error)
}

var _ Replayer = &stateReplayer{}

// chainer is responsible for supplying the chain components necessary to rebuild a state,
// namely a starting BeaconState and all available blocks from the starting state up to and including the target slot
// chainer负责提供chain components，对于构建一个state是必须的
// 从一个starting BeaconState以及所有可用的blocks，从starting state直到包含target slot
type chainer interface {
	chainForSlot(ctx context.Context, target primitives.Slot) (state.BeaconState, []interfaces.ReadOnlySignedBeaconBlock, error)
}

type stateReplayer struct {
	target primitives.Slot
	// 指定了方法？
	method  retrievalMethod
	chainer chainer
}

// ReplayBlocks applies all the blocks that were accumulated when building the Replayer.
// ReplayBlocks应用所有在构建Replayer时积累的blocks
// This method relies on the correctness of the code that constructed the Replayer data.
// 这个方法依赖构建Replayer data的代码的正确性
func (rs *stateReplayer) ReplayBlocks(ctx context.Context) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stateGen.stateReplayer.ReplayBlocks")
	defer span.End()

	var s state.BeaconState
	var descendants []interfaces.ReadOnlySignedBeaconBlock
	var err error
	switch rs.method {
	case forSlot:
		s, descendants, err = rs.chainer.chainForSlot(ctx, rs.target)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("Replayer initialized using unknown state retrieval method")
	}

	start := time.Now()
	diff, err := rs.target.SafeSubSlot(s.Slot())
	if err != nil {
		msg := fmt.Sprintf("error subtracting state.slot %d from replay target slot %d", s.Slot(), rs.target)
		return nil, errors.Wrap(err, msg)
	}
	log.WithFields(logrus.Fields{
		"startSlot": s.Slot(),
		"endSlot":   rs.target,
		"diff":      diff,
		// 从最新的state开始重放canonical blocks
	}).Debug("Replaying canonical blocks from most recent state")

	for _, b := range descendants {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 执行state transition
		s, err = executeStateTransitionStateGen(ctx, s, b)
		if err != nil {
			return nil, err
		}
	}
	if rs.target > s.Slot() {
		// 重新处理slots
		s, err = ReplayProcessSlots(ctx, s, rs.target)
		if err != nil {
			return nil, err
		}
	}

	duration := time.Since(start)
	log.WithFields(logrus.Fields{
		"duration": duration,
	}).Debug("Finished calling process_blocks on all blocks in ReplayBlocks")
	replayBlocksSummary.Observe(float64(duration.Milliseconds()))
	return s, nil
}

// ReplayToSlot invokes ReplayBlocks under the hood,
// but then also runs process_slots to advance the state past the root or slot used in the builder.
// for example, if you wanted the state to be at the target slot, but only integrating blocks up to
// slot-1, you could request Builder.ReplayerForSlot(slot-1).ReplayToSlot(slot)
func (rs *stateReplayer) ReplayToSlot(ctx context.Context, replayTo primitives.Slot) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stateGen.stateReplayer.ReplayToSlot")
	defer span.End()

	s, err := rs.ReplayBlocks(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ReplayBlocks")
	}
	if replayTo < s.Slot() {
		return nil, errors.Wrapf(ErrReplayTargetSlotExceeded, "slot desired=%d, state.slot=%d", replayTo, s.Slot())
	}
	if replayTo == s.Slot() {
		return s, nil
	}

	start := time.Now()
	log.WithFields(logrus.Fields{
		"startSlot": s.Slot(),
		"endSlot":   replayTo,
		"diff":      replayTo - s.Slot(),
	}).Debug("calling process_slots on remaining slots")

	// err will be handled after the bookend log
	s, err = ReplayProcessSlots(ctx, s, replayTo)
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("ReplayToSlot failed to seek to slot %d after applying blocks", replayTo))
	}
	duration := time.Since(start)
	log.WithFields(logrus.Fields{
		"duration": duration,
	}).Debug("time spent in process_slots")
	replayToSlotSummary.Observe(float64(duration.Milliseconds()))

	return s, nil
}

// ReplayerBuilder creates a Replayer that can be used to obtain a state at a specified slot or root
// (only ForSlot implemented so far).
// ReplayerBuilder创建一个Replayer，可以用于获取在一个特定slot或者root的state
// See documentation on Replayer for more on how to use this to obtain pre/post-block states
type ReplayerBuilder interface {
	// ReplayerForSlot creates a builder that will create a state that includes blocks up to and including the requested slot
	// The resulting Replayer will always yield a state with .Slot=target; if there are skipped blocks
	// between the highest canonical block in the db and the target, the replayer will fast-forward past the intervening
	// slots via process_slots.
	// 如果在db中最高的canonical block到target之间有skipped blocks，replayer会通过process_slots快速处理中间的slots
	ReplayerForSlot(target primitives.Slot) Replayer
}
