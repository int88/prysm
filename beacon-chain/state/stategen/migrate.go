package stategen

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

// MigrateToCold advances the finalized info in between the cold and hot state sections.
// MigrateToCold在cold和hot state sections之间移动finalized info
// It moves the recent finalized states from the hot section to the cold section and
// only preserves the ones that are on archived point.
// 它移动最近的finalized states，从hot section到cold section并且只保留位于archived point的
func (s *State) MigrateToCold(ctx context.Context, fRoot [32]byte) error {
	ctx, span := trace.StartSpan(ctx, "stateGen.MigrateToCold")
	defer span.End()

	// When migrating states we choose to acquire the migration lock before
	// proceeding. This is to prevent multiple migration routines from overwriting each
	// other.
	s.migrationLock.Lock()
	defer s.migrationLock.Unlock()

	s.finalizedInfo.lock.RLock()
	oldFSlot := s.finalizedInfo.slot
	s.finalizedInfo.lock.RUnlock()

	fBlock, err := s.beaconDB.Block(ctx, fRoot)
	if err != nil {
		return err
	}
	fSlot := fBlock.Block().Slot()
	if oldFSlot > fSlot {
		return nil
	}

	// Start at previous finalized slot, stop at current finalized slot (it will be handled in the next migration).
	// If the slot is on archived point, save the state of that slot to the DB.
	// 从之前的finalized slot开始，停止在当前的finalized slot（它会在下一个migration中进行处理）
	// 如果slot是一个archive point，保存slot到DB中
	for slot := oldFSlot; slot < fSlot; slot++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if slot%s.slotsPerArchivedPoint == 0 && slot != 0 {
			// 不能从cache中根据slot获取epoch boundary
			cached, exists, err := s.epochBoundaryStateCache.getBySlot(slot)
			if err != nil {
				return fmt.Errorf("could not get epoch boundary state for slot %d", slot)
			}

			var aRoot [32]byte
			var aState state.BeaconState

			// When the epoch boundary state is not in cache due to skip slot scenario,
			// we have to regenerate the state which will represent epoch boundary.
			// 当由于skip slot的情况，epoch boundary state不存在于cache中，我们需要重新生成
			// state，它会代表epoch boundary
			// By finding the highest available block below epoch boundary slot, we
			// generate the state for that block root.
			// 通过找到epoch boundary slot以下的最高可用的block，我们为这个block root生成state
			if exists {
				aRoot = cached.root
				aState = cached.state
			} else {
				_, roots, err := s.beaconDB.HighestRootsBelowSlot(ctx, slot)
				if err != nil {
					return err
				}
				// Given the block has been finalized, the db should not have more than one block in a given slot.
				// We should error out when this happens.
				// 给定block已经finalized，db不应该对给定的slot包含超过一个block
				// 我们应该报错，如果这发生的话
				if len(roots) != 1 {
					return errUnknownBlock
				}
				aRoot = roots[0]
				// There's no need to generate the state if the state already exists in the DB.
				// We can skip saving the state.
				// 如果state已经在DB中，我们不需要生成state，我们可以跳过对这个state的保存
				if !s.beaconDB.HasState(ctx, aRoot) {
					aState, err = s.StateByRoot(ctx, aRoot)
					if err != nil {
						return err
					}
				}
			}

			if s.beaconDB.HasState(ctx, aRoot) {
				// If you are migrating a state and its already part of the hot state cache saved to the db,
				// you can just remove it from the hot state cache as it becomes redundant.
				// 如果正在迁移一个state并且它已经是hot state cache，保存到db中的一部分，可以将它从hot state cache
				// 中移除，因为它冗余了
				s.saveHotStateDB.lock.Lock()
				roots := s.saveHotStateDB.blockRootsOfSavedStates
				for i := 0; i < len(roots); i++ {
					if aRoot == roots[i] {
						s.saveHotStateDB.blockRootsOfSavedStates = append(roots[:i], roots[i+1:]...)
						// There shouldn't be duplicated roots in `blockRootsOfSavedStates`.
						// Break here is ok.
						// 在`blockRootsOfSavedStates`不应该有重复的roots，因此这里可以break
						break
					}
				}
				s.saveHotStateDB.lock.Unlock()
				continue
			}

			// 保存state到DB中
			if err := s.beaconDB.SaveState(ctx, aState, aRoot); err != nil {
				return err
			}
			log.WithFields(
				logrus.Fields{
					"slot": aState.Slot(),
					"root": hex.EncodeToString(bytesutil.Trunc(aRoot[:])),
				}).Info("Saved state in DB")
		}
	}

	// Update finalized info in memory.
	// 更新内存中的finalized info
	fInfo, ok, err := s.epochBoundaryStateCache.getByBlockRoot(fRoot)
	if err != nil {
		return err
	}
	if ok {
		s.SaveFinalizedState(fSlot, fRoot, fInfo.state)
	}

	return nil
}
