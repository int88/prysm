package slashings

import (
	"context"
	"sync"

	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
)

// PoolInserter is capable of inserting new slashing objects into the operations pool.
// PoolInserter能够插入新的slashing对象到operations pool
type PoolInserter interface {
	InsertAttesterSlashing(
		ctx context.Context,
		state state.ReadOnlyBeaconState,
		slashing *ethpb.AttesterSlashing,
	) error
	InsertProposerSlashing(
		ctx context.Context,
		state state.ReadOnlyBeaconState,
		slashing *ethpb.ProposerSlashing,
	) error
}

// PoolManager maintains a pool of pending and recently included attester and proposer slashings.
// This pool is used by proposers to insert data into new blocks.
// PoolManager维护了一个pending以及最近添加的attester以及proposer slashings的pool
// 这个pool由proposer使用来插入data到新的blocks
type PoolManager interface {
	PoolInserter
	PendingAttesterSlashings(ctx context.Context, state state.ReadOnlyBeaconState, noLimit bool) []*ethpb.AttesterSlashing
	PendingProposerSlashings(ctx context.Context, state state.ReadOnlyBeaconState, noLimit bool) []*ethpb.ProposerSlashing
	MarkIncludedAttesterSlashing(as *ethpb.AttesterSlashing)
	MarkIncludedProposerSlashing(ps *ethpb.ProposerSlashing)
}

// Pool is a concrete implementation of PoolManager.
// Pool是PoolManager的具体的实现
type Pool struct {
	lock                    sync.RWMutex
	pendingProposerSlashing []*ethpb.ProposerSlashing
	pendingAttesterSlashing []*PendingAttesterSlashing
	included                map[types.ValidatorIndex]bool
}

// PendingAttesterSlashing represents an attester slashing in the operation pool.
// Allows for easy binary searching of included validator indexes.
// PendingAttesterSlashing代表一个attester slashing，在operation pool，允许
// 对包含的validators索引进行简单的二分查找
type PendingAttesterSlashing struct {
	attesterSlashing *ethpb.AttesterSlashing
	validatorToSlash types.ValidatorIndex
}
