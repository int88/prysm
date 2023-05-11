package doublylinkedtree

import (
	"sync"

	"github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice"
	forkchoicetypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice/types"
	fieldparams "github.com/prysmaticlabs/prysm/v3/config/fieldparams"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
)

// ForkChoice defines the overall fork choice store which includes all block nodes, validator's latest votes and balances.
type ForkChoice struct {
	sync.RWMutex
	store *Store
	// 追踪单个validator的最新的vote
	votes []Vote // tracks individual validator's last vote.
	// 追踪单个validator的balances，最后在votes中计算
	balances []uint64 // tracks individual validator's balances last accounted in votes.
	// 追踪单个validator的last justified balances
	justifiedBalances []uint64 // tracks individual validator's last justified balances.
	// 追踪active validators的数量
	numActiveValidators uint64                      // tracks the total number of active validators.
	balancesByRoot      forkchoice.BalancesByRooter // handler to obtain balances for the state with a given root
}

// Store defines the fork choice store which includes block nodes and the last view of checkpoint information.
// Store定义了fork choice store，包括block nodes和checkpoint信息的最后视图
type Store struct {
	justifiedCheckpoint     *forkchoicetypes.Checkpoint // latest justified epoch in store.
	bestJustifiedCheckpoint *forkchoicetypes.Checkpoint // best justified checkpoint in store.
	// 最好的unrealized justified checkpoint
	unrealizedJustifiedCheckpoint *forkchoicetypes.Checkpoint  // best unrealized justified checkpoint in store.
	unrealizedFinalizedCheckpoint *forkchoicetypes.Checkpoint  // best unrealized finalized checkpoint in store.
	prevJustifiedCheckpoint       *forkchoicetypes.Checkpoint  // previous justified checkpoint in store.
	finalizedCheckpoint           *forkchoicetypes.Checkpoint  // latest finalized epoch in store.
	proposerBoostRoot             [fieldparams.RootLength]byte // latest block root that was boosted after being received in a timely manner.
	previousProposerBoostRoot     [fieldparams.RootLength]byte // previous block root that was boosted after being received in a timely manner.
	previousProposerBoostScore    uint64                       // previous proposer boosted root score.
	committeeWeight               uint64                       // tracks the total active validator balance divided by the number of slots per Epoch.
	treeRootNode                  *Node                        // the root node of the store tree.
	headNode                      *Node                        // last head Node
	// 通过root索引的nodes
	nodeByRoot map[[fieldparams.RootLength]byte]*Node // nodes indexed by roots.
	// 通过payload hash索引的nodes
	nodeByPayload       map[[fieldparams.RootLength]byte]*Node // nodes indexed by payload Hash
	slashedIndices      map[primitives.ValidatorIndex]bool     // the list of equivocating validator indices
	originRoot          [fieldparams.RootLength]byte           // The genesis block root
	genesisTime         uint64
	highestReceivedNode *Node // The highest slot node.
	// 在最后一个epoch中收到的blocks的slot
	receivedBlocksLastEpoch [fieldparams.SlotsPerEpoch]primitives.Slot // Using `highestReceivedSlot`. The slot of blocks received in the last epoch.
	allTipsAreInvalid       bool                                       // tracks if all tips are not viable for head
}

// Node defines the individual block which includes its block parent, ancestor and how much weight accounted for it.
// This is used as an array based stateful DAG for efficient fork choice look up.
// Node定义了单个的block，它包含它的block parent，ancestor以及它有多少的weight
// 这用于一个基于array的stateful DAG，用于高效地查找fork choice
type Node struct {
	slot           primitives.Slot              // slot of the block converted to the node.
	root           [fieldparams.RootLength]byte // root of the block converted to the node.
	payloadHash    [fieldparams.RootLength]byte // payloadHash of the block converted to the node.
	parent         *Node                        // parent index of this node.
	children       []*Node                      // the list of direct children of this Node
	justifiedEpoch primitives.Epoch             // justifiedEpoch of this node.
	// 如果block被推进到下一个epoch，就会被justified
	unrealizedJustifiedEpoch primitives.Epoch // the epoch that would be justified if the block would be advanced to the next epoch.
	finalizedEpoch           primitives.Epoch // finalizedEpoch of this node.
	// 如果block被推进到下一个epoch，就会被finalized
	unrealizedFinalizedEpoch primitives.Epoch // the epoch that would be finalized if the block would be advanced to the next epoch.
	balance                  uint64           // the balance that voted for this node directly
	// node的weight: 包括children的总balance
	weight         uint64 // weight of this node: the total balance including children
	bestDescendant *Node  // bestDescendant node of this node.
	optimistic     bool   // whether the block has been fully validated or not
	timestamp      uint64 // The timestamp when the node was inserted.
}

// Vote defines an individual validator's vote.
// Vote定义了单个validator的vote
type Vote struct {
	// 当前vote的epoch
	currentRoot [fieldparams.RootLength]byte // current voting root.
	// 下一个vote的epoch
	nextRoot [fieldparams.RootLength]byte // next voting root.
	// 下一个voting period的epoch
	nextEpoch primitives.Epoch // epoch of next voting period.
}
