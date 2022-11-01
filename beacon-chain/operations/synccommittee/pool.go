package synccommittee

import (
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
)

var _ = Pool(&Store{})

// Pool defines the necessary methods for Prysm sync pool to serve
// validators. In the current design, aggregated attestations
// are used by proposers and sync committee messages are used by
// sync aggregators.
// Pool定义了Prysm sync pool必须的方法来服务validators
// 在当前的设计中，aggregated attestations由proposers使用并且sync committee messages由
// sync aggregators使用
type Pool interface {
	// Methods for Sync Contributions.
	// 用于同步contributions的方法
	SaveSyncCommitteeContribution(contr *ethpb.SyncCommitteeContribution) error
	SyncCommitteeContributions(slot types.Slot) ([]*ethpb.SyncCommitteeContribution, error)

	// Methods for Sync Committee Messages.
	// 用于Sync Committee Messages的方法
	SaveSyncCommitteeMessage(sig *ethpb.SyncCommitteeMessage) error
	SyncCommitteeMessages(slot types.Slot) ([]*ethpb.SyncCommitteeMessage, error)
}

// NewPool returns the sync committee store fulfilling the pool interface.
// NewPool返回sync committee store，实现了pool接口
func NewPool() Pool {
	return NewStore()
}
