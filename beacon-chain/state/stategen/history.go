package stategen

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"go.opencensus.io/trace"
)

func WithCache(c CachedGetter) CanonicalHistoryOption {
	return func(h *CanonicalHistory) {
		h.cache = c
	}
}

type CanonicalHistoryOption func(*CanonicalHistory)

func NewCanonicalHistory(h HistoryAccessor, cc CanonicalChecker, cs CurrentSlotter, opts ...CanonicalHistoryOption) *CanonicalHistory {
	ch := &CanonicalHistory{
		h:  h,
		cc: cc,
		cs: cs,
	}
	for _, o := range opts {
		o(ch)
	}
	return ch
}

type CanonicalHistory struct {
	h     HistoryAccessor
	cc    CanonicalChecker
	cs    CurrentSlotter
	cache CachedGetter
}

func (c *CanonicalHistory) ReplayerForSlot(target primitives.Slot) Replayer {
	return &stateReplayer{chainer: c, method: forSlot, target: target}
}

func (c *CanonicalHistory) BlockRootForSlot(ctx context.Context, target primitives.Slot) ([32]byte, error) {
	if currentSlot := c.cs.CurrentSlot(); target > currentSlot {
		return [32]byte{}, errors.Wrap(ErrFutureSlotRequested, fmt.Sprintf("requested=%d, current=%d", target, currentSlot))
	}

	slotAbove := target + 1
	// don't bother searching for candidate roots when we know the target slot is genesis
	// 我们不需要去寻找candidate roots，当我们知道target root是genesis
	for slotAbove > 1 {
		if ctx.Err() != nil {
			return [32]byte{}, errors.Wrap(ctx.Err(), "context canceled during canonicalBlockForSlot")
		}
		slot, roots, err := c.h.HighestRootsBelowSlot(ctx, slotAbove)
		if err != nil {
			return [32]byte{}, errors.Wrap(err, fmt.Sprintf("error finding highest block w/ slot < %d", slotAbove))
		}
		if len(roots) == 0 {
			return [32]byte{}, errors.Wrap(ErrNoBlocksBelowSlot, fmt.Sprintf("slot=%d", slotAbove))
		}
		r, err := c.bestForSlot(ctx, roots)
		if err == nil {
			// we found a valid, canonical block!
			// 我们找到一个合法的canonical block
			return r, nil
		}

		// we found a block, but it wasn't considered canonical - keep looking
		// 我们找到一个block，但是它不是canonical，继续查找
		if errors.Is(err, ErrNoCanonicalBlockForSlot) {
			// break once we've seen slot 0 (and prevent underflow)
			// 退出，如果我们已经到slot 0
			if slot == params.BeaconConfig().GenesisSlot {
				// genesis block，跳出
				break
			}
			slotAbove = slot
			continue
		}
		return [32]byte{}, err
	}

	return c.h.GenesisBlockRoot(ctx)
}

// bestForSlot encapsulates several messy realities of the underlying db code, looping through multiple blocks,
// performing null/validity checks, and using CanonicalChecker to only pick canonical blocks.
// bestForSlot封装了底层db代码中混乱的现实，遍历多个blocks，执行null/validity checks并且使用CanonicalChecker来
// 只选择canonical blocks
func (c *CanonicalHistory) bestForSlot(ctx context.Context, roots [][32]byte) ([32]byte, error) {
	for _, root := range roots {
		canon, err := c.cc.IsCanonical(ctx, root)
		if err != nil {
			return [32]byte{}, errors.Wrap(err, "replayer could not check if block is canonical")
		}
		if canon {
			return root, nil
		}
	}
	return [32]byte{}, errors.Wrap(ErrNoCanonicalBlockForSlot, "no good block for slot")
}

// ChainForSlot creates a value that satisfies the Replayer interface via db queries
// and the stategen transition helper methods. This implementation uses the following algorithm:
// ChainForSlot创建一个value，满足Replayer接口，通过db的请求以及stategen transition的帮助方法，使用如下的算法
// - find the highest canonical block <= the target slot
// - 找到最大的canonical block <= the target slot
// - starting with this block, recursively search backwards for a stored state, and accumulate intervening blocks
// - 从这个block开始，递归地往后搜索一个stored state，并且积累中间的blocks
func (c *CanonicalHistory) chainForSlot(ctx context.Context, target primitives.Slot) (state.BeaconState, []interfaces.ReadOnlySignedBeaconBlock, error) {
	ctx, span := trace.StartSpan(ctx, "canonicalChainer.chainForSlot")
	defer span.End()
	// 获取target slot的block root
	r, err := c.BlockRootForSlot(ctx, target)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "no canonical block root found below slot=%d", target)
	}
	// 获取block root对应的block
	b, err := c.h.Block(ctx, r)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "unable to retrieve canonical block for slot, root=%#x", r)
	}
	s, descendants, err := c.ancestorChain(ctx, b)
	if err != nil {
		// 查询ancestor以及descendant blocks失败
		return nil, nil, errors.Wrap(err, "failed to query for ancestor and descendant blocks")
	}

	return s, descendants, nil
}

func (c *CanonicalHistory) getState(ctx context.Context, blockRoot [32]byte) (state.BeaconState, error) {
	if c.cache != nil {
		// 从缓存中获取block root对应的state
		st, err := c.cache.ByBlockRoot(blockRoot)
		if err == nil {
			return st, nil
		}
		if !errors.Is(err, ErrNotInCache) {
			return nil, errors.Wrap(err, "error reading from state cache during state replay")
		}
	}
	return c.h.StateOrError(ctx, blockRoot)
}

// ancestorChain works backwards through the chain lineage, accumulating blocks and checking for a saved state.
// If it finds a saved state that the tail block was descended from, it returns this state and
// all blocks in the lineage, including the tail block. Blocks are returned in ascending order.
// Note that this function assumes that the tail is a canonical block, and therefore assumes that
// all ancestors are also canonical.
// ancestorChain通过chain lineage向后构建，累计blocks并且检查一个保存的state
// 如果它找到一个保存的state，是作为tail block的祖先，它返回这个state并且所有的blocks，包含tail block
// blocks按照升序返回，注意，这个函数假设tail是一个canonical block，因此假设所有的祖先都是canonical的
func (c *CanonicalHistory) ancestorChain(ctx context.Context, tail interfaces.ReadOnlySignedBeaconBlock) (state.BeaconState, []interfaces.ReadOnlySignedBeaconBlock, error) {
	ctx, span := trace.StartSpan(ctx, "canonicalChainer.ancestorChain")
	defer span.End()
	chain := make([]interfaces.ReadOnlySignedBeaconBlock, 0)
	for {
		if err := ctx.Err(); err != nil {
			msg := fmt.Sprintf("context canceled while finding ancestors of block at slot %d", tail.Block().Slot())
			return nil, nil, errors.Wrap(err, msg)
		}
		b := tail.Block()
		// compute hash_tree_root of current block and try to look up the corresponding state
		// 计算当前block的hash_tree_root并且试着查找对应的state
		root, err := b.HashTreeRoot()
		if err != nil {
			msg := fmt.Sprintf("could not compute htr for descendant block at slot=%d", b.Slot())
			return nil, nil, errors.Wrap(err, msg)
		}
		st, err := c.getState(ctx, root)
		// err == nil, we've got a real state - the job is done!
		// Note: in cases where there are skipped slots we could find a state that is a descendant
		// of the block we are searching for. We don't want to return a future block, so in this case
		// we keep working backwards.
		if err == nil && st.Slot() == b.Slot() {
			// we found the state by the root of the head, meaning it has already been applied.
			// we only want to return the blocks descended from it.
			reverseChain(chain)
			return st, chain, nil
		}
		// ErrNotFoundState errors are fine, but other errors mean something is wrong with the db
		// ErrNotFoundState是ok的，但是其他的errors意味着db有问题
		if err != nil && !errors.Is(err, db.ErrNotFoundState) {
			return nil, nil, errors.Wrap(err, fmt.Sprintf("error querying database for state w/ block root = %#x", root))
		}
		// 查找parent
		parent, err := c.h.Block(ctx, b.ParentRoot())
		if err != nil {
			msg := fmt.Sprintf("db error when retrieving parent of block at slot=%d by root=%#x", b.Slot(), b.ParentRoot())
			return nil, nil, errors.Wrap(err, msg)
		}
		if blocks.BeaconBlockIsNil(parent) != nil {
			msg := fmt.Sprintf("unable to retrieve parent of block at slot=%d by root=%#x", b.Slot(), b.ParentRoot())
			return nil, nil, errors.Wrap(db.ErrNotFound, msg)
		}
		chain = append(chain, tail)
		// 用parent给tail赋值
		tail = parent
	}
}

func reverseChain(c []interfaces.ReadOnlySignedBeaconBlock) {
	last := len(c) - 1
	swaps := (last + 1) / 2
	for i := 0; i < swaps; i++ {
		c[i], c[last-i] = c[last-i], c[i]
	}
}
