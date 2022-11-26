package doublylinkedtree

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/config/features"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/time/slots"
)

// NewSlot mimics the implementation of `on_tick` in fork choice consensus spec.
// NewSlot模拟了`on_tick`的实现，在fork choice consensus spec
// It resets the proposer boost root in fork choice, and it updates store's justified checkpoint
// if a better checkpoint on the store's finalized checkpoint chain.
// 它重置了在fork choice中的proposer boost root，并且它更新store的justified checkpoint，如果一个
// 更好的checkpoint在store的finalized checkpoint chain
// This should only be called at the start of every slot interval.
// 这个函数只应该在每个slot interval的开始被调用
//
// Spec pseudocode definition:
//    # Reset store.proposer_boost_root if this is a new slot
//    # 重置store.proposer_boost_root，如果这是一个新的slot
//    if current_slot > previous_slot:
//        store.proposer_boost_root = Root()
//
//    # Not a new epoch, return
//    # 不是一个新的epoch，返回
//    if not (current_slot > previous_slot and compute_slots_since_epoch_start(current_slot) == 0):
//        return
//
//    # Update store.justified_checkpoint if a better checkpoint on the store.finalized_checkpoint chain
//    # 更新store.justified_checkpoint，如果在store.finalized_checkpoint chain中有一个更好的checkpoint
//    if store.best_justified_checkpoint.epoch > store.justified_checkpoint.epoch:
//        finalized_slot = compute_start_slot_at_epoch(store.finalized_checkpoint.epoch)
//        ancestor_at_finalized_slot = get_ancestor(store, store.best_justified_checkpoint.root, finalized_slot)
//        if ancestor_at_finalized_slot == store.finalized_checkpoint.root:
//            store.justified_checkpoint = store.best_justified_checkpoint
func (f *ForkChoice) NewSlot(ctx context.Context, slot types.Slot) error {
	// Reset proposer boost root
	if err := f.ResetBoostedProposerRoot(ctx); err != nil {
		return errors.Wrap(err, "could not reset boosted proposer root in fork choice")
	}

	// Return if it's not a new epoch.
	// 不是一个新的epoch，直接返回
	if !slots.IsEpochStart(slot) {
		return nil
	}

	// Update store.justified_checkpoint if a better checkpoint on the store.finalized_checkpoint chain
	// 更新store.justified_checkpoint，如果在store.finalized_checkpoint chain中有一个更好的checkpoint
	f.store.checkpointsLock.Lock()
	defer f.store.checkpointsLock.Unlock()

	bjcp := f.store.bestJustifiedCheckpoint
	jcp := f.store.justifiedCheckpoint
	fcp := f.store.finalizedCheckpoint
	if bjcp.Epoch > jcp.Epoch {
		finalizedSlot, err := slots.EpochStart(fcp.Epoch)
		if err != nil {
			return err
		}

		// We check that the best justified checkpoint is a descendant of the finalized checkpoint.
		// This should always happen as forkchoice enforces that every node is a descendant of the
		// finalized checkpoint. This check is here for additional security, consider removing the extra
		// loop call here.
		// 我们检查best justified checkpoint是finalized checkpoint的descendant
		r, err := f.AncestorRoot(ctx, bjcp.Root, finalizedSlot)
		if err != nil {
			return err
		}
		if r == fcp.Root {
			f.store.justifiedCheckpoint = bjcp
		}
	}
	if features.Get().PullTips {
		f.UpdateUnrealizedCheckpoints()
	}
	return nil
}
