package stategen

import (
	"context"
	"math"

	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/config/params"
	"github.com/prysmaticlabs/prysm/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/time/slots"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

// SaveState saves the state in the cache and/or DB.
// SaveState保存state到cache或者DB中
func (s *State) SaveState(ctx context.Context, blockRoot [32]byte, st state.BeaconState) error {
	ctx, span := trace.StartSpan(ctx, "stateGen.SaveState")
	defer span.End()

	return s.saveStateByRoot(ctx, blockRoot, st)
}

// ForceCheckpoint initiates a cold state save of the given block root's state. This method does not update the
// "last archived state" but simply saves the specified state from the root argument into the DB.
// ForceCheckpoint初始化一个cold state save，对于给定block的root的state，这个方法没有更新"last archived state"，但是简单地
// 保存了特定的state，从root argument到DB
//
// The name "Checkpoint" isn't referring to checkpoint in the sense of our consensus type, but checkpoint for our historical states.
// 名字中的"Checkpoint"不是引用的我们的共识类型中的checkpoint，而是对于历史状态的checkpoint
func (s *State) ForceCheckpoint(ctx context.Context, blockRoot []byte) error {
	ctx, span := trace.StartSpan(ctx, "stateGen.ForceCheckpoint")
	defer span.End()

	root32 := bytesutil.ToBytes32(blockRoot)
	// Before the first finalized checkpoint, the finalized root is zero hash.
	// Return early if there hasn't been a finalized checkpoint.
	// 在第一个finalized checkpoint之前，finalized root是zero hash，尽早返回如果
	// 还没有一个finalized checkpoint
	if root32 == params.BeaconConfig().ZeroHash {
		return nil
	}

	fs, err := s.loadStateByRoot(ctx, root32)
	if err != nil {
		return err
	}

	return s.beaconDB.SaveState(ctx, fs, root32)
}

// This saves a post beacon state. On the epoch boundary,
// it saves a full state. On an intermediate slot, it saves a back pointer to the
// nearest epoch boundary state.
// 这保存一个post beacon state，在epoch boundary，它保存一个full state，在一个中间的slot，它保存
// 一个back pointer，指向最近的boundary state
func (s *State) saveStateByRoot(ctx context.Context, blockRoot [32]byte, st state.BeaconState) error {
	ctx, span := trace.StartSpan(ctx, "stateGen.saveStateByRoot")
	defer span.End()

	// Duration can't be 0 to prevent panic for division.
	duration := uint64(math.Max(float64(s.saveHotStateDB.duration), 1))

	s.saveHotStateDB.lock.Lock()
	if s.saveHotStateDB.enabled && st.Slot().Mod(duration) == 0 {
		if err := s.beaconDB.SaveState(ctx, st, blockRoot); err != nil {
			s.saveHotStateDB.lock.Unlock()
			return err
		}
		s.saveHotStateDB.blockRootsOfSavedStates = append(s.saveHotStateDB.blockRootsOfSavedStates, blockRoot)

		log.WithFields(logrus.Fields{
			"slot":                   st.Slot(),
			"totalHotStateSavedInDB": len(s.saveHotStateDB.blockRootsOfSavedStates),
		}).Info("Saving hot state to DB")
	}
	s.saveHotStateDB.lock.Unlock()

	// If the hot state is already in cache, one can be sure the state was processed and in the DB.
	// 如果hot state已经在缓存中，我们可以确保state已经处理并且在DB中
	if s.hotStateCache.has(blockRoot) {
		return nil
	}

	// Only on an epoch boundary slot, save epoch boundary state in epoch boundary root state cache.
	// 只有一个epoch boundary slot，保存epoch boundary state在epoch boundary root state cache
	if slots.IsEpochStart(st.Slot()) {
		if err := s.epochBoundaryStateCache.put(blockRoot, st); err != nil {
			return err
		}
	}

	// On an intermediate slot, save state summary.
	// 在一个中间的slot，保存state summary
	if err := s.beaconDB.SaveStateSummary(ctx, &ethpb.StateSummary{
		Slot: st.Slot(),
		Root: blockRoot[:],
	}); err != nil {
		return err
	}

	// Store the copied state in the hot state cache.
	// 保存copied state到hot state cache
	s.hotStateCache.put(blockRoot, st)

	return nil
}

// EnableSaveHotStateToDB enters the mode that saves hot beacon state to the DB.
// This usually gets triggered when there's long duration since finality.
func (s *State) EnableSaveHotStateToDB(_ context.Context) {
	s.saveHotStateDB.lock.Lock()
	defer s.saveHotStateDB.lock.Unlock()
	if s.saveHotStateDB.enabled {
		return
	}

	s.saveHotStateDB.enabled = true

	log.WithFields(logrus.Fields{
		"enabled":       s.saveHotStateDB.enabled,
		"slotsInterval": s.saveHotStateDB.duration,
	}).Warn("Entering mode to save hot states in DB")
}

// DisableSaveHotStateToDB exits the mode that saves beacon state to DB for the hot states.
// This usually gets triggered once there's finality after long duration since finality.
func (s *State) DisableSaveHotStateToDB(ctx context.Context) error {
	s.saveHotStateDB.lock.Lock()
	defer s.saveHotStateDB.lock.Unlock()
	if !s.saveHotStateDB.enabled {
		return nil
	}

	log.WithFields(logrus.Fields{
		"enabled":          s.saveHotStateDB.enabled,
		"deletedHotStates": len(s.saveHotStateDB.blockRootsOfSavedStates),
	}).Warn("Exiting mode to save hot states in DB")

	// Delete previous saved states in DB as we are turning this mode off.
	s.saveHotStateDB.enabled = false
	if err := s.beaconDB.DeleteStates(ctx, s.saveHotStateDB.blockRootsOfSavedStates); err != nil {
		return err
	}
	s.saveHotStateDB.blockRootsOfSavedStates = nil

	return nil
}
