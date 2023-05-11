package state_native

import (
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stateutil"
	"github.com/prysmaticlabs/prysm/v3/runtime/version"
)

// CurrentEpochParticipation corresponding to participation bits on the beacon chain.
// CurrentEpochParticipation对应于beacon chain上的participation bits
func (b *BeaconState) CurrentEpochParticipation() ([]byte, error) {
	if b.version == version.Phase0 {
		return nil, errNotSupported("CurrentEpochParticipation", b.version)
	}

	if b.currentEpochParticipation == nil {
		return nil, nil
	}

	b.lock.RLock()
	defer b.lock.RUnlock()

	return b.currentEpochParticipationVal(), nil
}

// PreviousEpochParticipation corresponding to participation bits on the beacon chain.
func (b *BeaconState) PreviousEpochParticipation() ([]byte, error) {
	if b.version == version.Phase0 {
		return nil, errNotSupported("PreviousEpochParticipation", b.version)
	}

	if b.previousEpochParticipation == nil {
		return nil, nil
	}

	b.lock.RLock()
	defer b.lock.RUnlock()

	return b.previousEpochParticipationVal(), nil
}

// UnrealizedCheckpointBalances returns the total balances: active, target attested in
// current epoch and target attested in previous epoch. This function is used to
// compute the "unrealized justification" that a synced Beacon Block will have.
// UnrealizedCheckpointBalances返回总余额：当前epoch中的active，target attested以及上一个epoch中的target attested
// 这个函数用于计算同步的Beacon Block将会有的“unrealized justification”
func (b *BeaconState) UnrealizedCheckpointBalances() (uint64, uint64, uint64, error) {
	if b.version == version.Phase0 {
		return 0, 0, 0, errNotSupported("UnrealizedCheckpointBalances", b.version)
	}

	currentEpoch := time.CurrentEpoch(b)
	b.lock.RLock()
	defer b.lock.RUnlock()

	cp := b.currentEpochParticipation
	pp := b.previousEpochParticipation
	if cp == nil || pp == nil {
		return 0, 0, 0, ErrNilParticipation
	}

	return stateutil.UnrealizedCheckpointBalances(cp, pp, b.validators, currentEpoch)

}

// currentEpochParticipationVal corresponding to participation bits on the beacon chain.
// This assumes that a lock is already held on BeaconState.
func (b *BeaconState) currentEpochParticipationVal() []byte {
	tmp := make([]byte, len(b.currentEpochParticipation))
	copy(tmp, b.currentEpochParticipation)
	return tmp
}

// previousEpochParticipationVal corresponding to participation bits on the beacon chain.
// This assumes that a lock is already held on BeaconState.
func (b *BeaconState) previousEpochParticipationVal() []byte {
	tmp := make([]byte, len(b.previousEpochParticipation))
	copy(tmp, b.previousEpochParticipation)
	return tmp
}
