package doublylinkedtree

import (
	"bytes"
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/config/features"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	v1 "github.com/prysmaticlabs/prysm/v3/proto/eth/v1"
)

// applyWeightChanges recomputes the weight of the node passed as an argument and all of its descendants,
// using the current balance stored in each node. This function requires a lock
// in Store.nodesLock
// applyWeightChanges重新计算node的weight，使用当前存储在每个node的当前的balance，这个函数需要一个
// Store.nodesLock
func (n *Node) applyWeightChanges(ctx context.Context) error {
	// Recursively calling the children to sum their weights.
	// 遍历地调用children来累加它们的weights
	childrenWeight := uint64(0)
	for _, child := range n.children {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := child.applyWeightChanges(ctx); err != nil {
			return err
		}
		childrenWeight += child.weight
	}
	if n.root == params.BeaconConfig().ZeroHash {
		return nil
	}
	// 自己的balance，加上childrenWeight
	n.weight = n.balance + childrenWeight
	return nil
}

// updateBestDescendant updates the best descendant of this node and its
// children. This function assumes the caller has a lock on Store.nodesLock
// updateBestDescendant更新这个node的best descendant以及它的children，这个函数假设
// caller对于Store.nodesLock有lock
func (n *Node) updateBestDescendant(ctx context.Context, justifiedEpoch, finalizedEpoch, currentEpoch types.Epoch) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(n.children) == 0 {
		// 如果没有children，则设置bestDescendant为nil
		n.bestDescendant = nil
		return nil
	}

	var bestChild *Node
	bestWeight := uint64(0)
	hasViableDescendant := false
	for _, child := range n.children {
		if child == nil {
			return errors.Wrap(ErrNilNode, "could not update best descendant")
		}
		// 递归获得best descendant
		if err := child.updateBestDescendant(ctx, justifiedEpoch, finalizedEpoch, currentEpoch); err != nil {
			return err
		}
		childLeadsToViableHead := child.leadsToViableHead(justifiedEpoch, finalizedEpoch, currentEpoch)
		if childLeadsToViableHead && !hasViableDescendant {
			// The child leads to a viable head, but the current
			// parent's best child doesn't.
			// 这个child引导到viable head，但是当前的parent的best child没有
			bestWeight = child.weight
			bestChild = child
			hasViableDescendant = true
		} else if childLeadsToViableHead {
			// If both are viable, compare their weights.
			// 如果都是可行的，比较它们的weight
			if child.weight == bestWeight {
				// Tie-breaker of equal weights by root.
				// 通过比root的大小，决定best child
				if bytes.Compare(child.root[:], bestChild.root[:]) > 0 {
					bestChild = child
				}
			} else if child.weight > bestWeight {
				bestChild = child
				bestWeight = child.weight
			}
		}
	}
	if hasViableDescendant {
		if bestChild.bestDescendant == nil {
			n.bestDescendant = bestChild
		} else {
			n.bestDescendant = bestChild.bestDescendant
		}
	} else {
		n.bestDescendant = nil
	}
	return nil
}

// viableForHead returns true if the node is viable to head.
// Any node with different finalized or justified epoch than
// the ones in fork choice store should not be viable to head.
// viableForHead返回true，如果node对于head是可行的，任何node，如果和fork choice store
// 有着不同的finalized或者justified epoch，则对于Head是不可行的
func (n *Node) viableForHead(justifiedEpoch, finalizedEpoch, currentEpoch types.Epoch) bool {
	justified := justifiedEpoch == n.justifiedEpoch || justifiedEpoch == 0
	finalized := finalizedEpoch == n.finalizedEpoch || finalizedEpoch == 0
	if features.Get().EnableDefensivePull && !justified && justifiedEpoch+1 == currentEpoch {
		if n.unrealizedJustifiedEpoch+1 >= currentEpoch {
			justified = true
		}
		if n.unrealizedFinalizedEpoch >= finalizedEpoch {
			finalized = true
		}
	}
	// 两者必须相等
	return justified && finalized
}

func (n *Node) leadsToViableHead(justifiedEpoch, finalizedEpoch, currentEpoch types.Epoch) bool {
	if n.bestDescendant == nil {
		// 如果没有best descendant，则判断node自己是不是能作head
		return n.viableForHead(justifiedEpoch, finalizedEpoch, currentEpoch)
	}
	// 否则根据best descendant判断有没有能当head
	return n.bestDescendant.viableForHead(justifiedEpoch, finalizedEpoch, currentEpoch)
}

// setNodeAndParentValidated sets the current node and all the ancestors as validated (i.e. non-optimistic).
// setNodeAndParentValidated将当前的node和所有的ancestors标记为validated（例如，non-optimistic）
func (n *Node) setNodeAndParentValidated(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if !n.optimistic {
		return nil
	}
	// 将node自己的optimistic设置为false
	n.optimistic = false

	if n.parent == nil {
		return nil
	}
	return n.parent.setNodeAndParentValidated(ctx)
}

// nodeTreeDump appends to the given list all the nodes descending from this one
// nodeTreeDump扩展给定的list，从这个list派生而来的所有nodes
func (n *Node) nodeTreeDump(ctx context.Context, nodes []*v1.ForkChoiceNode) ([]*v1.ForkChoiceNode, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var parentRoot [32]byte
	if n.parent != nil {
		parentRoot = n.parent.root
	}
	thisNode := &v1.ForkChoiceNode{
		Slot:                     n.slot,
		BlockRoot:                n.root[:],
		ParentRoot:               parentRoot[:],
		JustifiedEpoch:           n.justifiedEpoch,
		FinalizedEpoch:           n.finalizedEpoch,
		UnrealizedJustifiedEpoch: n.unrealizedJustifiedEpoch,
		UnrealizedFinalizedEpoch: n.unrealizedFinalizedEpoch,
		Balance:                  n.balance,
		Weight:                   n.weight,
		ExecutionOptimistic:      n.optimistic,
		ExecutionBlockHash:       n.payloadHash[:],
		Timestamp:                n.timestamp,
	}
	if n.optimistic {
		thisNode.Validity = v1.ForkChoiceNodeValidity_OPTIMISTIC
	} else {
		thisNode.Validity = v1.ForkChoiceNodeValidity_VALID
	}

	nodes = append(nodes, thisNode)
	var err error
	for _, child := range n.children {
		// 遍历子节点
		nodes, err = child.nodeTreeDump(ctx, nodes)
		if err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// VotedFraction returns the fraction of the committee that voted directly for
// this node.
// VotedFraction返回committee的分数，直接投票给这个node
func (f *ForkChoice) VotedFraction(root [32]byte) (uint64, error) {
	f.store.nodesLock.RLock()
	defer f.store.nodesLock.RUnlock()

	// Avoid division by zero before a block is inserted.
	// 在block插入之前，避免除以0
	if f.store.committeeBalance == 0 {
		return 0, nil
	}

	node, ok := f.store.nodeByRoot[root]
	if !ok || node == nil {
		return 0, ErrNilNode
	}
	// 节点的balance除以committee balance
	return node.balance * 100 / f.store.committeeBalance, nil
}
