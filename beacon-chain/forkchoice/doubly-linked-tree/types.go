package doublylinkedtree

import (
	"sync"

	forkchoicetypes "github.com/prysmaticlabs/prysm/beacon-chain/forkchoice/types"
	fieldparams "github.com/prysmaticlabs/prysm/config/fieldparams"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
)

// ForkChoice defines the overall fork choice store which includes all block nodes, validator's latest votes and balances.
// ForkChoice定义了整体的fork choice store，其中包括block nodes, validator最新的votes以及balances
type ForkChoice struct {
	store *Store
	// 追踪单个validator的last vote
	votes     []Vote // tracks individual validator's last vote.
	votesLock sync.RWMutex
	// 追踪单个validator的last jutified balances
	balances []uint64 // tracks individual validator's last justified balances.
}

// Store defines the fork choice store which includes block nodes and the last view of checkpoint information.
// Store定义了fork choice store，包括block nodes以及checkpoint信息的最新view
type Store struct {
	justifiedCheckpoint           *forkchoicetypes.Checkpoint // latest justified epoch in store.
	bestJustifiedCheckpoint       *forkchoicetypes.Checkpoint // best justified checkpoint in store.
	unrealizedJustifiedCheckpoint *forkchoicetypes.Checkpoint // best unrealized justified checkpoint in store.
	unrealizedFinalizedCheckpoint *forkchoicetypes.Checkpoint // best unrealized finalized checkpoint in store.
	prevJustifiedCheckpoint       *forkchoicetypes.Checkpoint // previous justified checkpoint in store.
	// store中最新的finalized epoch
	finalizedCheckpoint *forkchoicetypes.Checkpoint // latest finalized epoch in store.
	pruneThreshold      uint64                      // do not prune tree unless threshold is reached.
	// 最新被boosted block root，在以一个timely mannner被接收之后
	proposerBoostRoot [fieldparams.RootLength]byte // latest block root that was boosted after being received in a timely manner.
	// 上一个block root，在及时接收之后
	previousProposerBoostRoot [fieldparams.RootLength]byte // previous block root that was boosted after being received in a timely manner.
	// 上一个proposer boosted root score
	previousProposerBoostScore uint64 // previous proposer boosted root score.
	// store tree的root node
	treeRootNode *Node // the root node of the store tree.
	headNode     *Node // last head Node
	// 通过roots进行索引的nodes
	nodeByRoot map[[fieldparams.RootLength]byte]*Node // nodes indexed by roots.
	// 通过payload Hash进行索引的nodes
	nodeByPayload map[[fieldparams.RootLength]byte]*Node // nodes indexed by payload Hash
	// 一系列模棱两可的validator索引
	slashedIndices    map[types.ValidatorIndex]bool // the list of equivocating validator indices
	originRoot        [fieldparams.RootLength]byte  // The genesis block root
	nodesLock         sync.RWMutex
	proposerBoostLock sync.RWMutex
	checkpointsLock   sync.RWMutex
	genesisTime       uint64
}

// Node defines the individual block which includes its block parent, ancestor and how much weight accounted for it.
// This is used as an array based stateful DAG for efficient fork choice look up.
// Node定义了individual block，它包括block parent, ancestor以及包含多少weight
// 这用于一个基于array的stateful DAG用于有效的fork choice look up
type Node struct {
	slot        types.Slot                   // slot of the block converted to the node.
	root        [fieldparams.RootLength]byte // root of the block converted to the node.
	payloadHash [fieldparams.RootLength]byte // payloadHash of the block converted to the node.
	// 这个节点的parent index
	parent *Node // parent index of this node.
	// 这个Node的direct children的列表
	children []*Node // the list of direct children of this Node
	// 这个node的justifiedEpoch
	justifiedEpoch types.Epoch // justifiedEpoch of this node.
	// 这个epoch会被justified，如果block被移动到下一个epoch
	unrealizedJustifiedEpoch types.Epoch // the epoch that would be justified if the block would be advanced to the next epoch.
	finalizedEpoch           types.Epoch // finalizedEpoch of this node.
	// 这个epoch会被finalized，如果block被移动到下一个epoch
	unrealizedFinalizedEpoch types.Epoch // the epoch that would be finalized if the block would be advanced to the next epoch.
	// 直接投向这个node的balance
	balance uint64 // the balance that voted for this node directly
	// 这个节点的weight：包括children的total balance
	weight uint64 // weight of this node: the total balance including children
	// 这个node的bestDescendant
	bestDescendant *Node // bestDescendant node of this node.
	// block是否被完全校验
	optimistic bool // whether the block has been fully validated or not
}

// Vote defines an individual validator's vote.
// Vote定义了单个validator的vote
type Vote struct {
	// 当前的voting root
	currentRoot [fieldparams.RootLength]byte // current voting root.
	// 下一个voting root
	nextRoot [fieldparams.RootLength]byte // next voting root.
	// 下一个voting period的epoch
	nextEpoch types.Epoch // epoch of next voting period.
}
