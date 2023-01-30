package doublylinkedtree

import (
	"sync"

	"github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice"
	forkchoicetypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice/types"
	fieldparams "github.com/prysmaticlabs/prysm/v3/config/fieldparams"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
)

// ForkChoice defines the overall fork choice store which includes all block nodes, validator's latest votes and balances.
// ForkChoice定义了总体的fork choice store，包括所有的block nodes，validator的最新的votes以及balances
type ForkChoice struct {
	store *Store
	// 追踪单个validator的最后的vote
	votes     []Vote // tracks individual validator's last vote.
	votesLock sync.RWMutex
	// 追踪单个validator的最新的justified balances
	balances []uint64 // tracks individual validator's last justified balances.
	// handler用于获取state的balances，对于一个给定的root
	balancesByRoot forkchoice.BalancesByRooter // handler to obtain balances for the state with a given root
}

// Store defines the fork choice store which includes block nodes and the last view of checkpoint information.
// Store定义了fork choice store，包含block nodes以及checkpoint信息的last view
type Store struct {
	// store中最后justified epoch
	justifiedCheckpoint *forkchoicetypes.Checkpoint // latest justified epoch in store.
	// store中的best justified checkpoint
	bestJustifiedCheckpoint *forkchoicetypes.Checkpoint // best justified checkpoint in store.
	// 在store中unrealized justified和finalized checkpoint
	unrealizedJustifiedCheckpoint *forkchoicetypes.Checkpoint // best unrealized justified checkpoint in store.
	unrealizedFinalizedCheckpoint *forkchoicetypes.Checkpoint // best unrealized finalized checkpoint in store.
	prevJustifiedCheckpoint       *forkchoicetypes.Checkpoint // previous justified checkpoint in store.
	// 在store中最后finalized的epoch
	finalizedCheckpoint       *forkchoicetypes.Checkpoint  // latest finalized epoch in store.
	proposerBoostRoot         [fieldparams.RootLength]byte // latest block root that was boosted after being received in a timely manner.
	previousProposerBoostRoot [fieldparams.RootLength]byte // previous block root that was boosted after being received in a timely manner.
	// 之前的proposer的boosted root score
	previousProposerBoostScore uint64 // previous proposer boosted root score.
	// store tree的root node
	treeRootNode *Node                                  // the root node of the store tree.
	headNode     *Node                                  // last head Node
	nodeByRoot   map[[fieldparams.RootLength]byte]*Node // nodes indexed by roots.
	// 通过payload hash进行索引的nodes
	nodeByPayload       map[[fieldparams.RootLength]byte]*Node // nodes indexed by payload Hash
	slashedIndices      map[types.ValidatorIndex]bool          // the list of equivocating validator indices
	originRoot          [fieldparams.RootLength]byte           // The genesis block root
	nodesLock           sync.RWMutex
	proposerBoostLock   sync.RWMutex
	checkpointsLock     sync.RWMutex
	genesisTime         uint64
	highestReceivedNode *Node // The highest slot node.
	// 使用`highestReceivedSlot`，在最后一个epoch接收到的blocks的slot
	receivedBlocksLastEpoch [fieldparams.SlotsPerEpoch]types.Slot // Using `highestReceivedSlot`. The slot of blocks received in the last epoch.
	// 追踪是否所有的tips都不能当head
	allTipsAreInvalid bool // tracks if all tips are not viable for head
	// 追踪所有active validator的balance，除以slots，对于每个epoch，需要一个锁，对nodes进行读写
	committeeBalance uint64 // tracks the total active validator balance divided by slots per epoch. Requires a lock on nodes to read/write
}

// Node defines the individual block which includes its block parent, ancestor and how much weight accounted for it.
// This is used as an array based stateful DAG for efficient fork choice look up.
// Node定义了单个的block，包含它的block parent，ancestor以及它的重量占了多少
// 它作为一个基于array的有状态DAG使用，用于高效查找fork choice
type Node struct {
	slot        types.Slot                   // slot of the block converted to the node.
	root        [fieldparams.RootLength]byte // root of the block converted to the node.
	payloadHash [fieldparams.RootLength]byte // payloadHash of the block converted to the node.
	parent      *Node                        // parent index of this node.
	// 这个节点的direct children
	children       []*Node     // the list of direct children of this Node
	justifiedEpoch types.Epoch // justifiedEpoch of this node.
	// 这个epoch会被justified，如果block会被推进到下一个epoch
	unrealizedJustifiedEpoch types.Epoch // the epoch that would be justified if the block would be advanced to the next epoch.
	// 这个node的finalizedEpoch
	finalizedEpoch           types.Epoch // finalizedEpoch of this node.
	unrealizedFinalizedEpoch types.Epoch // the epoch that would be finalized if the block would be advanced to the next epoch.
	// 直接投票给这个节点的balance
	balance uint64 // the balance that voted for this node directly
	// 这个node的weight，包括children的total balance
	weight uint64 // weight of this node: the total balance including children
	// 这个节点的best descendant
	bestDescendant *Node // bestDescendant node of this node.
	// 是否这个block被完整校验
	optimistic bool // whether the block has been fully validated or not
	// 这个节点被插入时的时间戳
	timestamp uint64 // The timestamp when the node was inserted.
}

// Vote defines an individual validator's vote.
// Vote代表单个validator的vote
type Vote struct {
	// 当前的voting root
	currentRoot [fieldparams.RootLength]byte // current voting root.
	// 下一个voting root
	nextRoot [fieldparams.RootLength]byte // next voting root.
	// 下一个voting period的epoch
	nextEpoch types.Epoch // epoch of next voting period.
}
