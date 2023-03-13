package state_native

import (
	"github.com/pkg/errors"
	customtypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/state/state-native/custom-types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/state-native/types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stateutil"
	fieldparams "github.com/prysmaticlabs/prysm/v3/config/fieldparams"
)

// SetStateRoots for the beacon state. Updates the state roots
// to a new value by overwriting the previous value.
// SetStateRoots为beacon state设置state roots，更新state roots到一个新的值
// 通过覆盖之前的
func (b *BeaconState) SetStateRoots(val [][]byte) error {
	b.lock.Lock()
	defer b.lock.Unlock()

	b.sharedFieldReferences[types.StateRoots].MinusRef()
	b.sharedFieldReferences[types.StateRoots] = stateutil.NewRef(1)

	// SLOTS_PER_HISTORICAL_ROOT，即8192个
	var rootsArr [fieldparams.StateRootsLength][32]byte
	for i := 0; i < len(rootsArr); i++ {
		copy(rootsArr[i][:], val[i])
	}
	roots := customtypes.StateRoots(rootsArr)
	// 设置roots
	b.stateRoots = &roots
	b.markFieldAsDirty(types.StateRoots)
	b.rebuildTrie[types.StateRoots] = true
	return nil
}

// UpdateStateRootAtIndex for the beacon state. Updates the state root
// at a specific index to a new value.
// UpdateStateRootAtIndex更新beacon state，更新特定索引的state root到一个新的值
func (b *BeaconState) UpdateStateRootAtIndex(idx uint64, stateRoot [32]byte) error {
	b.lock.RLock()
	if uint64(len(b.stateRoots)) <= idx {
		b.lock.RUnlock()
		return errors.Errorf("invalid index provided %d", idx)
	}
	b.lock.RUnlock()

	b.lock.Lock()
	defer b.lock.Unlock()

	// Check if we hold the only reference to the shared state roots slice.
	// 检查我们对于共享的state root slice有唯一的引用
	r := b.stateRoots
	if ref := b.sharedFieldReferences[types.StateRoots]; ref.Refs() > 1 {
		// Copy elements in underlying array by reference.
		roots := *b.stateRoots
		rootsCopy := roots
		r = &rootsCopy
		ref.MinusRef()
		b.sharedFieldReferences[types.StateRoots] = stateutil.NewRef(1)
	}

	r[idx] = stateRoot
	b.stateRoots = r

	b.markFieldAsDirty(types.StateRoots)
	b.addDirtyIndices(types.StateRoots, []uint64{idx})
	return nil
}
