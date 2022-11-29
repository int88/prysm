package doublylinkedtree

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/blocks"
	"github.com/prysmaticlabs/prysm/beacon-chain/forkchoice"
	forkchoicetypes "github.com/prysmaticlabs/prysm/beacon-chain/forkchoice/types"
	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/config/features"
	fieldparams "github.com/prysmaticlabs/prysm/config/fieldparams"
	"github.com/prysmaticlabs/prysm/config/params"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/runtime/version"
	"github.com/prysmaticlabs/prysm/time/slots"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

// New initializes a new fork choice store.
// New初始化一个新的fork choice store
func New() *ForkChoice {
	s := &Store{
		justifiedCheckpoint:           &forkchoicetypes.Checkpoint{},
		bestJustifiedCheckpoint:       &forkchoicetypes.Checkpoint{},
		unrealizedJustifiedCheckpoint: &forkchoicetypes.Checkpoint{},
		unrealizedFinalizedCheckpoint: &forkchoicetypes.Checkpoint{},
		prevJustifiedCheckpoint:       &forkchoicetypes.Checkpoint{},
		finalizedCheckpoint:           &forkchoicetypes.Checkpoint{},
		proposerBoostRoot:             [32]byte{},
		// 从Root映射到Node
		nodeByRoot: make(map[[fieldparams.RootLength]byte]*Node),
		// 从payload映射到Node
		nodeByPayload:  make(map[[fieldparams.RootLength]byte]*Node),
		slashedIndices: make(map[types.ValidatorIndex]bool),
		pruneThreshold: defaultPruneThreshold,
	}

	b := make([]uint64, 0)
	v := make([]Vote, 0)
	return &ForkChoice{store: s, balances: b, votes: v}
}

// NodeCount returns the current number of nodes in the Store.
// NodeCount返回在Store中存储的nodes的数目
func (f *ForkChoice) NodeCount() int {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()
	// 获取nodes的数目
	return len(f.store.nodeByRoot)
}

// Head returns the head root from fork choice store.
// Head返回fork choice store的head root
// It firsts computes validator's balance changes then recalculates block tree from leaves to root.
// 它首先计算validator的balance changes，之后再重新从leaves到root计算block tree
func (f *ForkChoice) Head(
	ctx context.Context,
	justifiedStateBalances []uint64,
) ([32]byte, error) {
	ctx, span := trace.StartSpan(ctx, "doublyLinkedForkchoice.Head")
	defer span.End()
	// 先把vouteLock锁上
	f.votesLock.Lock()
	defer f.votesLock.Unlock()

	calledHeadCount.Inc()

	// Using the write lock here because `applyWeightChanges` that gets called subsequently requires a write operation.
	// 这里使用wrtie lock，因为`applyWeightChanges`会在后续被调用，需要一个写操作
	f.store.nodesLock.Lock()
	defer f.store.nodesLock.Unlock()

	if err := f.updateBalances(justifiedStateBalances); err != nil {
		return [32]byte{}, errors.Wrap(err, "could not update balances")
	}

	if err := f.store.applyProposerBoostScore(justifiedStateBalances); err != nil {
		// 不能应用proposer boost store
		return [32]byte{}, errors.Wrap(err, "could not apply proposer boost score")
	}

	if err := f.store.treeRootNode.applyWeightChanges(ctx); err != nil {
		// 不能应用weight的变更
		return [32]byte{}, errors.Wrap(err, "could not apply weight changes")
	}

	// 获取justified checkpoint和finalized checkpoint
	jc := f.JustifiedCheckpoint()
	fc := f.FinalizedCheckpoint()
	if err := f.store.treeRootNode.updateBestDescendant(ctx, jc.Epoch, fc.Epoch); err != nil {
		return [32]byte{}, errors.Wrap(err, "could not update best descendant")
	}
	return f.store.head(ctx)
}

// ProcessAttestation processes attestation for vote accounting, it iterates around validator indices
// and update their votes accordingly.
// ProcessAttestation处理attestation用于统计voting，它遍历validator indices并且相应地更新votes
func (f *ForkChoice) ProcessAttestation(ctx context.Context, validatorIndices []uint64, blockRoot [32]byte, targetEpoch types.Epoch) {
	_, span := trace.StartSpan(ctx, "doublyLinkedForkchoice.ProcessAttestation")
	defer span.End()
	f.votesLock.Lock()
	defer f.votesLock.Unlock()

	for _, index := range validatorIndices {
		// Validator indices will grow the vote cache.
		// Validator indices会让vote cache增长
		for index >= uint64(len(f.votes)) {
			// 持续添加vote，直到index能被索引到
			f.votes = append(f.votes, Vote{currentRoot: params.BeaconConfig().ZeroHash, nextRoot: params.BeaconConfig().ZeroHash})
		}

		// Newly allocated vote if the root fields are untouched.
		// 新分配的vote，如果root字段是untouched
		newVote := f.votes[index].nextRoot == params.BeaconConfig().ZeroHash &&
			f.votes[index].currentRoot == params.BeaconConfig().ZeroHash

		// Vote gets updated if it's newly allocated or high target epoch.
		// 更新Vote，如果这是新分配的或者high target epoch
		if newVote || targetEpoch > f.votes[index].nextEpoch {
			f.votes[index].nextEpoch = targetEpoch
			// 更改nextRoot为blockRoot
			f.votes[index].nextRoot = blockRoot
		}
	}

	processedAttestationCount.Inc()
}

// InsertNode processes a new block by inserting it to the fork choice store.
// InsertNode处理一个新的block，通过插入它到fork choice store
func (f *ForkChoice) InsertNode(ctx context.Context, state state.BeaconState, root [32]byte) error {
	ctx, span := trace.StartSpan(ctx, "doublyLinkedForkchoice.InsertNode")
	defer span.End()

	slot := state.Slot()
	bh := state.LatestBlockHeader()
	if bh == nil {
		return errNilBlockHeader
	}
	parentRoot := bytesutil.ToBytes32(bh.ParentRoot)
	payloadHash := [32]byte{}
	if state.Version() >= version.Bellatrix {
		ph, err := state.LatestExecutionPayloadHeader()
		if err != nil {
			return err
		}
		if ph != nil {
			// 将execution payload header导入？
			copy(payloadHash[:], ph.BlockHash)
		}
	}
	// 从state中获取jc和fc
	jc := state.CurrentJustifiedCheckpoint()
	if jc == nil {
		return errInvalidNilCheckpoint
	}
	justifiedEpoch := jc.Epoch
	fc := state.FinalizedCheckpoint()
	if fc == nil {
		return errInvalidNilCheckpoint
	}
	finalizedEpoch := fc.Epoch
	// 插入store
	node, err := f.store.insert(ctx, slot, root, parentRoot, payloadHash, justifiedEpoch, finalizedEpoch)
	if err != nil {
		return err
	}

	if features.Get().PullTips {
		jc, fc = f.store.pullTips(ctx, state, node, jc, fc)
	}
	return f.updateCheckpoints(ctx, jc, fc)
}

// updateCheckpoints update the checkpoints when inserting a new node.
// updateCheckpoints在插入一个新的node的时候更新checkpoints
func (f *ForkChoice) updateCheckpoints(ctx context.Context, jc, fc *ethpb.Checkpoint) error {
	f.store.checkpointsLock.Lock()
	if jc.Epoch > f.store.justifiedCheckpoint.Epoch {
		if jc.Epoch > f.store.bestJustifiedCheckpoint.Epoch {
			f.store.bestJustifiedCheckpoint = &forkchoicetypes.Checkpoint{Epoch: jc.Epoch,
				Root: bytesutil.ToBytes32(jc.Root)}
		}
		currentSlot := slots.CurrentSlot(f.store.genesisTime)
		if slots.SinceEpochStarts(currentSlot) < params.BeaconConfig().SafeSlotsToUpdateJustified {
			f.store.prevJustifiedCheckpoint = f.store.justifiedCheckpoint
			// 设置jc
			f.store.justifiedCheckpoint = &forkchoicetypes.Checkpoint{Epoch: jc.Epoch,
				Root: bytesutil.ToBytes32(jc.Root)}
		} else {
			currentJcp := f.store.justifiedCheckpoint
			currentRoot := currentJcp.Root
			if currentRoot == params.BeaconConfig().ZeroHash {
				currentRoot = f.store.originRoot
			}
			jSlot, err := slots.EpochStart(currentJcp.Epoch)
			if err != nil {
				f.store.checkpointsLock.Unlock()
				return err
			}
			jcRoot := bytesutil.ToBytes32(jc.Root)
			root, err := f.AncestorRoot(ctx, jcRoot, jSlot)
			if err != nil {
				f.store.checkpointsLock.Unlock()
				return err
			}
			if root == currentRoot {
				f.store.prevJustifiedCheckpoint = f.store.justifiedCheckpoint
				f.store.justifiedCheckpoint = &forkchoicetypes.Checkpoint{Epoch: jc.Epoch,
					Root: jcRoot}
			}
		}
	}
	// Update finalization
	// 更新finalization
	if fc.Epoch <= f.store.finalizedCheckpoint.Epoch {
		f.store.checkpointsLock.Unlock()
		return nil
	}
	// 更新finalized checkpoint和justified checkpoint
	f.store.finalizedCheckpoint = &forkchoicetypes.Checkpoint{Epoch: fc.Epoch,
		Root: bytesutil.ToBytes32(fc.Root)}
	f.store.justifiedCheckpoint = &forkchoicetypes.Checkpoint{Epoch: jc.Epoch,
		Root: bytesutil.ToBytes32(jc.Root)}
	f.store.checkpointsLock.Unlock()
	return f.store.prune(ctx)
}

// HasNode returns true if the node exists in fork choice store,
// false else wise.
// HasNode返回true，如果node在fork choice store中存在，否则返回false
func (f *ForkChoice) HasNode(root [32]byte) bool {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	_, ok := f.store.nodeByRoot[root]
	return ok
}

// HasParent returns true if the node parent exists in fork choice store,
// false else wise.
func (f *ForkChoice) HasParent(root [32]byte) bool {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		return false
	}

	return node.parent != nil
}

// IsCanonical returns true if the given root is part of the canonical chain.
// IsCanonical返回true，如果给定的root是canonical chain的一部分
func (f *ForkChoice) IsCanonical(root [32]byte) bool {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		return false
	}

	if node.bestDescendant == nil {
		if f.store.headNode.bestDescendant == nil {
			return node == f.store.headNode
		}
		return node == f.store.headNode.bestDescendant
	}
	if f.store.headNode.bestDescendant == nil {
		return node.bestDescendant == f.store.headNode
	}
	return node.bestDescendant == f.store.headNode.bestDescendant
}

// IsOptimistic returns true if the given root has been optimistically synced.
func (f *ForkChoice) IsOptimistic(root [32]byte) (bool, error) {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		return true, ErrNilNode
	}

	return node.optimistic, nil
}

// AncestorRoot returns the ancestor root of input block root at a given slot.
// AncestorRoot返回input block root的ancestor root，在一个给定的slot
func (f *ForkChoice) AncestorRoot(ctx context.Context, root [32]byte, slot types.Slot) ([32]byte, error) {
	ctx, span := trace.StartSpan(ctx, "protoArray.AncestorRoot")
	defer span.End()

	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		// 在nodeByRoot中找不到对应的节点
		return [32]byte{}, errors.Wrap(ErrNilNode, "could not determine ancestor root")
	}

	n := node
	for n != nil && n.slot > slot {
		if ctx.Err() != nil {
			return [32]byte{}, ctx.Err()
		}
		// 不断往上遍历
		n = n.parent
	}

	if n == nil {
		return [32]byte{}, errors.Wrap(ErrNilNode, "could not determine ancestor root")
	}

	return n.root, nil
}

// updateBalances updates the balances that directly voted for each block taking into account the
// validators' latest votes.
// updateBalances更新直接投票给每个block的balances，考虑validators的latest votes
func (f *ForkChoice) updateBalances(newBalances []uint64) error {
	for index, vote := range f.votes {
		// Skip if validator has been slashed
		// 跳过如果validator已经被slashed
		if f.store.slashedIndices[types.ValidatorIndex(index)] {
			continue
		}
		// Skip if validator has never voted for current root and next root (i.e. if the
		// votes are zero hash aka genesis block), there's nothing to compute.
		// 跳过如果validator没有投票给current root或者next root（例如投票给zero hash，也就是genesis block）
		// 没有什么需要计算
		if vote.currentRoot == params.BeaconConfig().ZeroHash && vote.nextRoot == params.BeaconConfig().ZeroHash {
			continue
		}

		oldBalance := uint64(0)
		newBalance := uint64(0)
		// If the validator index did not exist in `f.balances` or
		// `newBalances` list above, the balance is just 0.
		// 如果validator的index不存在于`f.balances`或者`newBalances`，balance就设置为0
		if index < len(f.balances) {
			oldBalance = f.balances[index]
		}
		if index < len(newBalances) {
			newBalance = newBalances[index]
		}

		// Update only if the validator's balance or vote has changed.
		// 更新，只有在validator的balance或者vote发生变更的时候
		if vote.currentRoot != vote.nextRoot || oldBalance != newBalance {
			// Ignore the vote if the root is not in fork choice
			// store, that means we have not seen the block before.
			// 忽略vote，如果root不再fork choice store，这意味着我们之前没看到过这个block
			nextNode, ok := f.store.nodeByRoot[vote.nextRoot]
			if ok && vote.nextRoot != params.BeaconConfig().ZeroHash {
				// Protection against nil node
				// 保护nil node
				if nextNode == nil {
					return errors.Wrap(ErrNilNode, "could not update balances")
				}
				nextNode.balance += newBalance
			}

			currentNode, ok := f.store.nodeByRoot[vote.currentRoot]
			if ok && vote.currentRoot != params.BeaconConfig().ZeroHash {
				// Protection against nil node
				// 保护nil node
				if currentNode == nil {
					return errors.Wrap(ErrNilNode, "could not update balances")
				}
				if currentNode.balance < oldBalance {
					f.store.proposerBoostLock.RLock()
					log.WithFields(logrus.Fields{
						"nodeRoot":                   fmt.Sprintf("%#x", bytesutil.Trunc(vote.currentRoot[:])),
						"oldBalance":                 oldBalance,
						"nodeBalance":                currentNode.balance,
						"nodeWeight":                 currentNode.weight,
						"proposerBoostRoot":          fmt.Sprintf("%#x", bytesutil.Trunc(f.store.proposerBoostRoot[:])),
						"previousProposerBoostRoot":  fmt.Sprintf("%#x", bytesutil.Trunc(f.store.previousProposerBoostRoot[:])),
						"previousProposerBoostScore": f.store.previousProposerBoostScore,
						// 有着非法balance的node，设置为0
					}).Warning("node with invalid balance, setting it to zero")
					f.store.proposerBoostLock.RUnlock()
					currentNode.balance = 0
				} else {
					// 减去oldBalance
					currentNode.balance -= oldBalance
				}
			}
		}

		// Rotate the validator vote.
		// 轮转validator vote
		f.votes[index].currentRoot = vote.nextRoot
	}
	f.balances = newBalances
	return nil
}

// Tips returns a list of possible heads from fork choice store, it returns the
// roots and the slots of the leaf nodes.
func (f *ForkChoice) Tips() ([][32]byte, []types.Slot) {
	return f.store.tips()
}

// ProposerBoost returns the proposerBoost of the store
func (f *ForkChoice) ProposerBoost() [fieldparams.RootLength]byte {
	return f.store.proposerBoost()
}

// SetOptimisticToValid sets the node with the given root as a fully validated node
// SetOptimisticToValid设置给定root的node作为full validated node
func (f *ForkChoice) SetOptimisticToValid(ctx context.Context, root [fieldparams.RootLength]byte) error {
	f.store.nodesLock.Lock()
	defer f.store.nodesLock.Unlock()
	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		return errors.Wrap(ErrNilNode, "could not set node to valid")
	}
	return node.setNodeAndParentValidated(ctx)
}

// BestJustifiedCheckpoint of fork choice store.
func (f *ForkChoice) BestJustifiedCheckpoint() *forkchoicetypes.Checkpoint {
	f.store.checkpointsLock.RLock()
	defer f.store.checkpointsLock.RUnlock()
	return f.store.bestJustifiedCheckpoint
}

// PreviousJustifiedCheckpoint of fork choice store.
func (f *ForkChoice) PreviousJustifiedCheckpoint() *forkchoicetypes.Checkpoint {
	f.store.checkpointsLock.RLock()
	defer f.store.checkpointsLock.RUnlock()
	return f.store.prevJustifiedCheckpoint
}

// JustifiedCheckpoint of fork choice store.
func (f *ForkChoice) JustifiedCheckpoint() *forkchoicetypes.Checkpoint {
	f.store.checkpointsLock.RLock()
	defer f.store.checkpointsLock.RUnlock()
	return f.store.justifiedCheckpoint
}

// FinalizedCheckpoint of fork choice store.
func (f *ForkChoice) FinalizedCheckpoint() *forkchoicetypes.Checkpoint {
	f.store.checkpointsLock.RLock()
	defer f.store.checkpointsLock.RUnlock()
	return f.store.finalizedCheckpoint
}

func (f *ForkChoice) ForkChoiceNodes() []*ethpb.ForkChoiceNode {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()
	ret := make([]*ethpb.ForkChoiceNode, len(f.store.nodeByRoot))
	return f.store.treeRootNode.rpcNodes(ret)
}

// SetOptimisticToInvalid removes a block with an invalid execution payload from fork choice store
func (f *ForkChoice) SetOptimisticToInvalid(ctx context.Context, root, parentRoot, payloadHash [fieldparams.RootLength]byte) ([][32]byte, error) {
	return f.store.setOptimisticToInvalid(ctx, root, parentRoot, payloadHash)
}

// InsertSlashedIndex adds the given slashed validator index to the
// store-tracked list. Votes from these validators are not accounted for
// in forkchoice.
func (f *ForkChoice) InsertSlashedIndex(_ context.Context, index types.ValidatorIndex) {
	f.store.nodesLock.Lock()
	defer f.store.nodesLock.Unlock()
	// return early if the index was already included:
	if f.store.slashedIndices[index] {
		return
	}
	f.store.slashedIndices[index] = true

	// Subtract last vote from this equivocating validator
	f.votesLock.RLock()
	defer f.votesLock.RUnlock()

	if index >= types.ValidatorIndex(len(f.balances)) {
		return
	}

	if index >= types.ValidatorIndex(len(f.votes)) {
		return
	}

	node, ok := f.store.nodeByRoot[f.votes[index].currentRoot]
	if !ok || node == nil {
		return
	}

	if node.balance < f.balances[index] {
		node.balance = 0
	} else {
		node.balance -= f.balances[index]
	}
}

// UpdateJustifiedCheckpoint sets the justified checkpoint to the given one
// UpdateJustifiedCheckpoint将justified checkpoint设置为给定值
func (f *ForkChoice) UpdateJustifiedCheckpoint(jc *forkchoicetypes.Checkpoint) error {
	if jc == nil {
		return errInvalidNilCheckpoint
	}
	f.store.checkpointsLock.Lock()
	defer f.store.checkpointsLock.Unlock()
	// 设置previous jc
	f.store.prevJustifiedCheckpoint = f.store.justifiedCheckpoint
	// 设置jc
	f.store.justifiedCheckpoint = jc
	bj := f.store.bestJustifiedCheckpoint
	if bj == nil || bj.Root == params.BeaconConfig().ZeroHash || jc.Epoch > bj.Epoch {
		// 设置bjc
		f.store.bestJustifiedCheckpoint = &forkchoicetypes.Checkpoint{Epoch: jc.Epoch, Root: jc.Root}
	}
	return nil
}

// UpdateFinalizedCheckpoint sets the finalized checkpoint to the given one
// UpdateFinalizedCheckpoint设置finalized checkpoint为给定值
func (f *ForkChoice) UpdateFinalizedCheckpoint(fc *forkchoicetypes.Checkpoint) error {
	if fc == nil {
		return errInvalidNilCheckpoint
	}
	f.store.checkpointsLock.Lock()
	defer f.store.checkpointsLock.Unlock()
	// 设置finalized checkpoint
	f.store.finalizedCheckpoint = fc
	return nil
}

// CommonAncestorRoot returns the common ancestor root between the two block roots r1 and r2.
func (f *ForkChoice) CommonAncestorRoot(ctx context.Context, r1 [32]byte, r2 [32]byte) ([32]byte, error) {
	ctx, span := trace.StartSpan(ctx, "doublelinkedtree.CommonAncestorRoot")
	defer span.End()

	// Do nothing if the input roots are the same.
	if r1 == r2 {
		return r1, nil
	}

	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	n1, ok := f.store.nodeByRoot[r1]
	if !ok || n1 == nil {
		return [32]byte{}, forkchoice.ErrUnknownCommonAncestor
	}
	n2, ok := f.store.nodeByRoot[r2]
	if !ok || n2 == nil {
		return [32]byte{}, forkchoice.ErrUnknownCommonAncestor
	}

	for {
		if ctx.Err() != nil {
			return [32]byte{}, ctx.Err()
		}
		if n1.slot > n2.slot {
			n1 = n1.parent
			// Reaches the end of the tree and unable to find common ancestor.
			// This should not happen at runtime as the finalized
			// node has to be a common ancestor
			if n1 == nil {
				return [32]byte{}, forkchoice.ErrUnknownCommonAncestor
			}
		} else {
			n2 = n2.parent
			// Reaches the end of the tree and unable to find common ancestor.
			if n2 == nil {
				return [32]byte{}, forkchoice.ErrUnknownCommonAncestor
			}
		}
		if n1 == n2 {
			return n1.root, nil
		}
	}
}

// InsertOptimisticChain inserts all nodes corresponding to blocks in the slice
// `blocks`. This slice must be ordered from child to parent. It includes all
// blocks **except** the first one (that is the one with the highest slot
// number). All blocks are assumed to be a strict chain
// where blocks[i].Parent = blocks[i+1]. Also we assume that the parent of the
// last block in this list is already included in forkchoice store.
func (f *ForkChoice) InsertOptimisticChain(ctx context.Context, chain []*forkchoicetypes.BlockAndCheckpoints) error {
	if len(chain) == 0 {
		return nil
	}
	for i := len(chain) - 1; i > 0; i-- {
		b := chain[i].Block
		r := bytesutil.ToBytes32(chain[i-1].Block.ParentRoot())
		parentRoot := bytesutil.ToBytes32(b.ParentRoot())
		payloadHash, err := blocks.GetBlockPayloadHash(b)
		if err != nil {
			return err
		}
		if _, err := f.store.insert(ctx,
			b.Slot(), r, parentRoot, payloadHash,
			chain[i].JustifiedCheckpoint.Epoch, chain[i].FinalizedCheckpoint.Epoch); err != nil {
			return err
		}
		if err := f.updateCheckpoints(ctx, chain[i].JustifiedCheckpoint, chain[i].FinalizedCheckpoint); err != nil {
			return err
		}
	}
	return nil
}

// SetGenesisTime sets the genesisTime tracked by forkchoice
func (f *ForkChoice) SetGenesisTime(genesisTime uint64) {
	f.store.genesisTime = genesisTime
}

// SetOriginRoot sets the genesis block root
func (f *ForkChoice) SetOriginRoot(root [32]byte) {
	f.store.originRoot = root
}

// CachedHeadRoot returns the last cached head root
func (f *ForkChoice) CachedHeadRoot() [32]byte {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()
	node := f.store.headNode
	if node == nil {
		return [32]byte{}
	}
	return f.store.headNode.root
}

// FinalizedPayloadBlockHash returns the hash of the payload at the finalized checkpoint
// FinalizedPayloadBlockHash返回在finalized checkpoint的hash of the payload
func (f *ForkChoice) FinalizedPayloadBlockHash() [32]byte {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()
	root := f.FinalizedCheckpoint().Root
	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		// This should not happen
		return [32]byte{}
	}
	return node.payloadHash
}

// JustifiedPayloadBlockHash returns the hash of the payload at the justified checkpoint
// JustifiedPayloadBlockHash返回在justified checkpoint的hash of the payload
func (f *ForkChoice) JustifiedPayloadBlockHash() [32]byte {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()
	root := f.JustifiedCheckpoint().Root
	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		// This should not happen
		return [32]byte{}
	}
	return node.payloadHash
}
