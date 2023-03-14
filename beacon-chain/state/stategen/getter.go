package stategen

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"go.opencensus.io/trace"
)

var ErrNoDataForSlot = errors.New("cannot retrieve data for slot")

// HasState returns true if the state exists in cache or in DB.
// HasState返回true，如果state存在于cache或者DB中
func (s *State) HasState(ctx context.Context, blockRoot [32]byte) (bool, error) {
	// 先判断cache中是不是有state
	has, err := s.hasStateInCache(ctx, blockRoot)
	if err != nil {
		return false, err
	}
	if has {
		return true, nil
	}
	// 再看db中有没有state
	return s.beaconDB.HasState(ctx, blockRoot), nil
}

// hasStateInCache returns true if the state exists in cache.
// hasStateInCache返回true，如果state存在于cache中
func (s *State) hasStateInCache(_ context.Context, blockRoot [32]byte) (bool, error) {
	// 先看hotStateCache，再看epochBoundaryStateCache
	if s.hotStateCache.has(blockRoot) {
		return true, nil
	}
	_, has, err := s.epochBoundaryStateCache.getByBlockRoot(blockRoot)
	if err != nil {
		return false, err
	}
	return has, nil
}

// StateByRootIfCachedNoCopy retrieves a state using the input block root only if the state is already in the cache.
// StateByRootIfCachedNoCopy获取一个state，使用输入的block root，只有state已经处于cache时
func (s *State) StateByRootIfCachedNoCopy(blockRoot [32]byte) state.BeaconState {
	if !s.hotStateCache.has(blockRoot) {
		return nil
	}
	return s.hotStateCache.getWithoutCopy(blockRoot)
}

// StateByRoot retrieves the state using input block root.
// StateByRoot获取state，使用输入的block root
func (s *State) StateByRoot(ctx context.Context, blockRoot [32]byte) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stateGen.StateByRoot")
	defer span.End()

	// Genesis case. If block root is zero hash, short circuit to use genesis state stored in DB.
	// Genesis的场景，如果block root是zero hash，直接使用存储在DB中的genesis state
	if blockRoot == params.BeaconConfig().ZeroHash {
		// 如果是zero hash，还是要找到block root?
		root, err := s.beaconDB.GenesisBlockRoot(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "could not get genesis block root")
		}
		blockRoot = root
	}
	return s.loadStateByRoot(ctx, blockRoot)
}

// ActiveNonSlashedBalancesByRoot retrieves the effective balances of all active and non-slashed validators at the
// state with a given root
// ActiveNonSlashedBalancesByRoot获取所有active以及没有被slash的validators的effective balances，在state中
// 对于一个给定的root
func (s *State) ActiveNonSlashedBalancesByRoot(ctx context.Context, blockRoot [32]byte) ([]uint64, error) {
	st, err := s.StateByRoot(ctx, blockRoot)
	if err != nil {
		return nil, err
	}
	if st == nil || st.IsNil() {
		return nil, errNilState
	}
	epoch := time.CurrentEpoch(st)

	balances := make([]uint64, st.NumValidators())
	var balanceAccretor = func(idx int, val state.ReadOnlyValidator) error {
		if helpers.IsActiveNonSlashedValidatorUsingTrie(val, epoch) {
			balances[idx] = val.EffectiveBalance()
		} else {
			balances[idx] = 0
		}
		return nil
	}
	// 从每个validator中读取
	if err := st.ReadFromEveryValidator(balanceAccretor); err != nil {
		return nil, err
	}
	return balances, nil
}

// StateByRootInitialSync retrieves the state from the DB for the initial syncing phase.
// StateByRootInitialSync从DB中获取state，用于初始的同步阶段
// It assumes initial syncing using a block list rather than a block tree hence the returned
// state is not copied (block batches returned from initial sync are linear).
// It invalidates cache for parent root because pre-state will get mutated.
// 它假设初始的同步。使用一个block list而不是一个block tree，因此返回的state不是被拷贝（从initial sync
// 返回的block batches是线性的），它invalidate cache，对于parent root，因为pre-state会被修改
//
// WARNING: Do not use this method for anything other than initial syncing purpose or block tree is applied.
func (s *State) StateByRootInitialSync(ctx context.Context, blockRoot [32]byte) (state.BeaconState, error) {
	// Genesis case. If block root is zero hash, short circuit to use genesis state stored in DB.
	if blockRoot == params.BeaconConfig().ZeroHash {
		return s.beaconDB.GenesisState(ctx)
	}

	// To invalidate cache for parent root because pre-state will get mutated.
	// 对于parent root，让cache invalidate，因为pre-state会被修改
	// It is a parent root because StateByRootInitialSync is always used to fetch the block's parent state.
	// 这是一个parent root，因为StateByRootInitialSync总是用于获取block的parent state
	defer s.hotStateCache.delete(blockRoot)

	if s.hotStateCache.has(blockRoot) {
		return s.hotStateCache.getWithoutCopy(blockRoot), nil
	}

	cachedInfo, ok, err := s.epochBoundaryStateCache.getByBlockRoot(blockRoot)
	if err != nil {
		return nil, err
	}
	if ok {
		return cachedInfo.state, nil
	}

	startState, err := s.latestAncestor(ctx, blockRoot)
	if err != nil {
		return nil, errors.Wrap(err, "could not get ancestor state")
	}
	if startState == nil || startState.IsNil() {
		return nil, errUnknownState
	}
	// 获取state summary
	summary, err := s.stateSummary(ctx, blockRoot)
	if err != nil {
		return nil, errors.Wrap(err, "could not get state summary")
	}
	// 如果start state就是summary的slot，直接返回
	if startState.Slot() == summary.Slot {
		return startState, nil
	}

	blks, err := s.loadBlocks(ctx, startState.Slot()+1, summary.Slot, bytesutil.ToBytes32(summary.Root))
	if err != nil {
		return nil, errors.Wrap(err, "could not load blocks")
	}
	startState, err = s.replayBlocks(ctx, startState, blks, summary.Slot)
	if err != nil {
		return nil, errors.Wrap(err, "could not replay blocks")
	}

	return startState, nil
}

// This returns the state summary object of a given block root. It first checks the cache, then checks the DB.
// 对于给定的block root返回state summary对象，它首先检查cache，之后检查DB
func (s *State) stateSummary(ctx context.Context, blockRoot [32]byte) (*ethpb.StateSummary, error) {
	var summary *ethpb.StateSummary
	var err error

	summary, err = s.beaconDB.StateSummary(ctx, blockRoot)
	if err != nil {
		return nil, err
	}

	if summary == nil {
		// 恢复state summary?
		return s.recoverStateSummary(ctx, blockRoot)
	}
	return summary, nil
}

// RecoverStateSummary recovers state summary object of a given block root by using the saved block in DB.
// RecoverStateSummary从一个给定的block root恢复state summary对象，使用DB中保存的block
func (s *State) recoverStateSummary(ctx context.Context, blockRoot [32]byte) (*ethpb.StateSummary, error) {
	if s.beaconDB.HasBlock(ctx, blockRoot) {
		// 确保db中有block
		b, err := s.beaconDB.Block(ctx, blockRoot)
		if err != nil {
			return nil, err
		}
		// 构建state summary
		summary := &ethpb.StateSummary{Slot: b.Block().Slot(), Root: blockRoot[:]}
		// 保存到db中
		if err := s.beaconDB.SaveStateSummary(ctx, summary); err != nil {
			return nil, err
		}
		return summary, nil
	}
	return nil, errors.New("could not find block in DB")
}

// DeleteStateFromCaches deletes the state from the caches.
// DeleteStateFromCaches从caches中删除state
func (s *State) DeleteStateFromCaches(_ context.Context, blockRoot [32]byte) error {
	s.hotStateCache.delete(blockRoot)
	return s.epochBoundaryStateCache.delete(blockRoot)
}

// This loads a beacon state from either the cache or DB, then replays blocks up the slot of the requested block root.
// 从cache或者DB中加载beacon state，之后重放blocks直到请求的block root所在的slot
func (s *State) loadStateByRoot(ctx context.Context, blockRoot [32]byte) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stateGen.loadStateByRoot")
	defer span.End()

	// First, it checks if the state exists in hot state cache.
	// 首先，检查state是不是存在于hot state cache
	cachedState := s.hotStateCache.get(blockRoot)
	if cachedState != nil && !cachedState.IsNil() {
		return cachedState, nil
	}

	// Second, it checks if the state exists in epoch boundary state cache.
	// 之后，检查state是否存在于epoch boundary state cache
	cachedInfo, ok, err := s.epochBoundaryStateCache.getByBlockRoot(blockRoot)
	if err != nil {
		return nil, err
	}
	if ok {
		return cachedInfo.state, nil
	}

	// Short circuit if the state is already in the DB.
	// 短路，如果state已经在DB中了
	if s.beaconDB.HasState(ctx, blockRoot) {
		return s.beaconDB.State(ctx, blockRoot)
	}

	// 不能获取state summary
	summary, err := s.stateSummary(ctx, blockRoot)
	if err != nil {
		return nil, errors.Wrap(err, "could not get state summary")
	}
	// 根据block root找到slot
	targetSlot := summary.Slot

	// Since the requested state is not in caches or DB, start replaying using the last
	// available ancestor state which is retrieved using input block's root.
	// 因为请求的state不在caches或者DB中，开始重放，使用最新可用的ancestor state，通过输入的
	// block的root获取
	startState, err := s.latestAncestor(ctx, blockRoot)
	if err != nil {
		return nil, errors.Wrap(err, "could not get ancestor state")
	}
	if startState == nil || startState.IsNil() {
		return nil, errUnknownBoundaryState
	}

	if startState.Slot() == targetSlot {
		return startState, nil
	}

	// 加载blocks
	blks, err := s.loadBlocks(ctx, startState.Slot()+1, targetSlot, bytesutil.ToBytes32(summary.Root))
	if err != nil {
		return nil, errors.Wrap(err, "could not load blocks for hot state using root")
	}

	replayBlockCount.Observe(float64(len(blks)))

	// 对blocks进行重发
	return s.replayBlocks(ctx, startState, blks, targetSlot)
}

// latestAncestor returns the highest available ancestor state of the input block root.
// It recursively looks up block's parent until a corresponding state of the block root
// is found in the caches or DB.
// latestAncestor返回最高可用的ancestor state，对于输入的block root，它递归地查找block的parent
// 直到对应的block root的state已经在缓存或者DB中找到
//
// There's three ways to derive block parent state:
// 有三种方式派生block parent state
// 1) block parent state is the last finalized state
// 1) block parent state是最新的finalized state
// 2) block parent state is the epoch boundary state and exists in epoch boundary cache
// 2) block parent state是epoch boundary state并且存在于epoch boundary cache
// 3) block parent state is in DB
// 3) block parent state在DB中
func (s *State) latestAncestor(ctx context.Context, blockRoot [32]byte) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "stateGen.latestAncestor")
	defer span.End()

	if s.isFinalizedRoot(blockRoot) && s.finalizedState() != nil {
		// 直接返回finalized state
		return s.finalizedState(), nil
	}

	b, err := s.beaconDB.Block(ctx, blockRoot)
	if err != nil {
		return nil, err
	}
	if err := blocks.BeaconBlockIsNil(b); err != nil {
		return nil, err
	}

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Is the state the genesis state.
		// 是否是genesis state
		parentRoot := b.Block().ParentRoot()
		if parentRoot == params.BeaconConfig().ZeroHash {
			s, err := s.beaconDB.GenesisState(ctx)
			return s, errors.Wrap(err, "could not get genesis state")
		}

		// Return an error if slot hasn't been covered by checkpoint sync.
		// 返回一个error，如果slot没有从checkpoint sync中恢复
		ps := b.Block().Slot() - 1
		if !s.slotAvailable(ps) {
			return nil, errors.Wrapf(ErrNoDataForSlot, "slot %d not in db due to checkpoint sync", ps)
		}
		// Does the state exist in the hot state cache.
		// state是否存在于hot state cache中
		if s.hotStateCache.has(parentRoot) {
			return s.hotStateCache.get(parentRoot), nil
		}

		// Does the state exist in finalized info cache.
		// state是否存在于finalized info cache中
		if s.isFinalizedRoot(parentRoot) {
			return s.finalizedState(), nil
		}

		// Does the state exist in epoch boundary cache.
		// state是否存在于epoch boundary cache中
		cachedInfo, ok, err := s.epochBoundaryStateCache.getByBlockRoot(parentRoot)
		if err != nil {
			return nil, err
		}
		if ok {
			return cachedInfo.state, nil
		}

		// Does the state exists in DB.
		// state是不是在DB中
		if s.beaconDB.HasState(ctx, parentRoot) {
			s, err := s.beaconDB.State(ctx, parentRoot)
			return s, errors.Wrap(err, "failed to retrieve state from db")
		}

		b, err = s.beaconDB.Block(ctx, parentRoot)
		if err != nil {
			// 是不是在block中
			return nil, errors.Wrap(err, "failed to retrieve block from db")
		}
		if b == nil || b.IsNil() {
			// block未知
			return nil, errUnknownBlock
		}
	}
}

func (s *State) CombinedCache() *CombinedCache {
	getters := make([]CachedGetter, 0)
	if s.hotStateCache != nil {
		getters = append(getters, s.hotStateCache)
	}
	if s.epochBoundaryStateCache != nil {
		getters = append(getters, s.epochBoundaryStateCache)
	}
	return &CombinedCache{getters: getters}
}

func (s *State) slotAvailable(slot primitives.Slot) bool {
	// default to assuming node was initialized from genesis - backfill only needs to be specified for checkpoint sync
	if s.backfillStatus == nil {
		return true
	}
	return s.backfillStatus.SlotCovered(slot)
}
