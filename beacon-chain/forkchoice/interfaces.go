package forkchoice

import (
	"context"

	forkchoicetypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice/types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	fieldparams "github.com/prysmaticlabs/prysm/v3/config/fieldparams"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	v1 "github.com/prysmaticlabs/prysm/v3/proto/eth/v1"
)

// BalanceByRooter is a handler to obtain the effective balances of the state
// with the given block root
type BalancesByRooter func(context.Context, [32]byte) ([]uint64, error)

// ForkChoicer represents the full fork choice interface composed of all the sub-interfaces.
// ForkChoicer代表一个完整的fork choice接口，由许多子接口组成
type ForkChoicer interface {
	// 计算head
	HeadRetriever // to compute head.
	// 追踪fork choice的新的block
	BlockProcessor // to track new block for fork choice.
	// 追踪fork choice的新的attestation
	AttestationProcessor // to track new attestation for fork choice.
	// 获取fork choice的信息
	Getter // to retrieve fork choice information.
	// 设置fork choice的信息
	Setter // to set fork choice information.
	// 及时提出block roots的能力
	ProposerBooster // ability to boost timely-proposed block roots.
}

// HeadRetriever retrieves head root and optimistic info of the current chain.
// HeadRetriever获取head root以及当前chain的optimistic信息
type HeadRetriever interface {
	Head(context.Context, []uint64) ([32]byte, error)
	CachedHeadRoot() [32]byte
	Tips() ([][32]byte, []types.Slot)
	IsOptimistic(root [32]byte) (bool, error)
	AllTipsAreInvalid() bool
}

// BlockProcessor processes the block that's used for accounting fork choice.
type BlockProcessor interface {
	InsertNode(context.Context, state.BeaconState, [32]byte) error
	InsertOptimisticChain(context.Context, []*forkchoicetypes.BlockAndCheckpoints) error
}

// AttestationProcessor processes the attestation that's used for accounting fork choice.
type AttestationProcessor interface {
	ProcessAttestation(context.Context, []uint64, [32]byte, types.Epoch)
	InsertSlashedIndex(context.Context, types.ValidatorIndex)
}

// ProposerBooster is able to boost the proposer's root score during fork choice.
// ProposerBooster能够在fork choice的时候，提出proposer的root score
type ProposerBooster interface {
	ResetBoostedProposerRoot(ctx context.Context) error
}

// Getter returns fork choice related information.
type Getter interface {
	HasNode([32]byte) bool
	ProposerBoost() [fieldparams.RootLength]byte
	HasParent(root [32]byte) bool
	AncestorRoot(ctx context.Context, root [32]byte, slot types.Slot) ([32]byte, error)
	CommonAncestor(ctx context.Context, root1 [32]byte, root2 [32]byte) ([32]byte, types.Slot, error)
	IsCanonical(root [32]byte) bool
	FinalizedCheckpoint() *forkchoicetypes.Checkpoint
	FinalizedPayloadBlockHash() [32]byte
	JustifiedCheckpoint() *forkchoicetypes.Checkpoint
	PreviousJustifiedCheckpoint() *forkchoicetypes.Checkpoint
	JustifiedPayloadBlockHash() [32]byte
	BestJustifiedCheckpoint() *forkchoicetypes.Checkpoint
	NodeCount() int
	HighestReceivedBlockSlot() types.Slot
	HighestReceivedBlockRoot() [32]byte
	ReceivedBlocksLastEpoch() (uint64, error)
	ForkChoiceDump(context.Context) (*v1.ForkChoiceDump, error)
	VotedFraction(root [32]byte) (uint64, error)
}

// Setter allows to set forkchoice information
// Setter允许设置forkchoice信息
type Setter interface {
	SetOptimisticToValid(context.Context, [fieldparams.RootLength]byte) error
	SetOptimisticToInvalid(context.Context, [fieldparams.RootLength]byte, [fieldparams.RootLength]byte, [fieldparams.RootLength]byte) ([][32]byte, error)
	UpdateJustifiedCheckpoint(*forkchoicetypes.Checkpoint) error
	UpdateFinalizedCheckpoint(*forkchoicetypes.Checkpoint) error
	SetGenesisTime(uint64)
	SetOriginRoot([32]byte)
	NewSlot(context.Context, types.Slot) error
	SetBalancesByRooter(BalancesByRooter)
}
