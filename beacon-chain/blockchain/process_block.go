package blockchain

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/async/event"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/blocks"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed"
	statefeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/state"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	coreTime "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition"
	forkchoicetypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice/types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/features"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	consensusblocks "github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/crypto/bls"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	"github.com/prysmaticlabs/prysm/v3/monitoring/tracing"
	ethpbv1 "github.com/prysmaticlabs/prysm/v3/proto/eth/v1"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1/attestation"
	"github.com/prysmaticlabs/prysm/v3/runtime/version"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"go.opencensus.io/trace"
)

// A custom slot deadline for processing state slots in our cache.
const slotDeadline = 5 * time.Second

// A custom deadline for deposit trie insertion.
const depositDeadline = 20 * time.Second

// This defines size of the upper bound for initial sync block cache.
var initialSyncBlockCacheSize = uint64(2 * params.BeaconConfig().SlotsPerEpoch)

// onBlock is called when a gossip block is received. It runs regular state transition on the block.
// The block's signing root should be computed before calling this method to avoid redundant
// computation in this method and methods it calls into.
// onBlock在收到一个gossip block的时候被调用，它运行常规的state transition，在block之上
//
// Spec pseudocode definition:
//
//		def on_block(store: Store, signed_block: SignedBeaconBlock) -> None:
//		 block = signed_block.message
//		 # Parent block must be known
//	  # Parent block必须是已知的
//		 assert block.parent_root in store.block_states
//		 # Make a copy of the state to avoid mutability issues
//	  # 对state进行拷贝来避免mutability issues
//		 pre_state = copy(store.block_states[block.parent_root])
//		 # Blocks cannot be in the future. If they are, their consideration must be delayed until the are in the past.
//	  # Blocks不能来自未来，如果是的话，对它们的操作必须延后，直到他们位于past
//		 assert get_current_slot(store) >= block.slot
//
//		 # Check that block is later than the finalized epoch slot (optimization to reduce calls to get_ancestor)
//	  # 检查block比finalized epoch  slot更晚
//		 finalized_slot = compute_start_slot_at_epoch(store.finalized_checkpoint.epoch)
//		 assert block.slot > finalized_slot
//		 # Check block is a descendant of the finalized block at the checkpoint finalized slot
//	  # 检查block是finalized block的后代，位于checkpoint finalized slot
//		 assert get_ancestor(store, block.parent_root, finalized_slot) == store.finalized_checkpoint.root
//
//		 # Check the block is valid and compute the post-state
//	  # 检查block是合法的并且计算post-state
//		 state = pre_state.copy()
//		 state_transition(state, signed_block, True)
//		 # Add new block to the store
//	  # 添加新的block到store
//		 store.blocks[hash_tree_root(block)] = block
//		 # Add new state for this block to the store
//	  # 添加这个block的state到store
//		 store.block_states[hash_tree_root(block)] = state
//
//		 # Update justified checkpoint
//	  # 更新justified checkpoint
//		 if state.current_justified_checkpoint.epoch > store.justified_checkpoint.epoch:
//		     if state.current_justified_checkpoint.epoch > store.best_justified_checkpoint.epoch:
//	          # 设置的是best justified checkpoint
//		         store.best_justified_checkpoint = state.current_justified_checkpoint
//		     if should_update_justified_checkpoint(store, state.current_justified_checkpoint):
//		         store.justified_checkpoint = state.current_justified_checkpoint
//
//		 # Update finalized checkpoint
//	  # 更新finalized checkpoint
//		 if state.finalized_checkpoint.epoch > store.finalized_checkpoint.epoch:
//		     store.finalized_checkpoint = state.finalized_checkpoint
//
//		     # Potentially update justified if different from store
//	      # 潜在地更新justified，如果和store中不同
//		     if store.justified_checkpoint != state.current_justified_checkpoint:
//		         # Update justified if new justified is later than store justified
//	          # 更新justified，如果新的justified比store justified更晚
//		         if state.current_justified_checkpoint.epoch > store.justified_checkpoint.epoch:
//		             store.justified_checkpoint = state.current_justified_checkpoint
//		             return
//
//		         # Update justified if store justified is not in chain with finalized checkpoint
//	          # 更新justified，如果存储的justified和finalized checkpoint不在一条链上
//		         finalized_slot = compute_start_slot_at_epoch(store.finalized_checkpoint.epoch)
//		         ancestor_at_finalized_slot = get_ancestor(store, store.justified_checkpoint.root, finalized_slot)
//		         if ancestor_at_finalized_slot != store.finalized_checkpoint.root:
//		             store.justified_checkpoint = state.current_justified_checkpoint
func (s *Service) onBlock(ctx context.Context, signed interfaces.SignedBeaconBlock, blockRoot [32]byte) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.onBlock")
	defer span.End()
	if err := consensusblocks.BeaconBlockIsNil(signed); err != nil {
		return invalidBlock{error: err}
	}
	startTime := time.Now()
	b := signed.Block()

	preState, err := s.getBlockPreState(ctx, b)
	if err != nil {
		return err
	}

	// Save current justified and finalized epochs for future use.
	// 保存当前的justified以及finalized epochs用于后续使用
	currStoreJustifiedEpoch := s.ForkChoicer().JustifiedCheckpoint().Epoch
	currStoreFinalizedEpoch := s.ForkChoicer().FinalizedCheckpoint().Epoch
	// prestate的finalized以及justified epoch
	preStateFinalizedEpoch := preState.FinalizedCheckpoint().Epoch
	preStateJustifiedEpoch := preState.CurrentJustifiedCheckpoint().Epoch

	preStateVersion, preStateHeader, err := getStateVersionAndPayload(preState)
	if err != nil {
		return err
	}
	stateTransitionStartTime := time.Now()
	// 运行state transition
	postState, err := transition.ExecuteStateTransition(ctx, preState, signed)
	if err != nil {
		return invalidBlock{error: err}
	}
	stateTransitionProcessingTime.Observe(float64(time.Since(stateTransitionStartTime).Milliseconds()))

	postStateVersion, postStateHeader, err := getStateVersionAndPayload(postState)
	if err != nil {
		return err
	}
	// 校验payload
	isValidPayload, err := s.notifyNewPayload(ctx, postStateVersion, postStateHeader, signed)
	if err != nil {
		return errors.Wrap(err, "could not validate new payload")
	}
	if isValidPayload {
		// 如果payload是合法的
		if err := s.validateMergeTransitionBlock(ctx, preStateVersion, preStateHeader, signed); err != nil {
			return err
		}
	}
	// 保存post state info
	if err := s.savePostStateInfo(ctx, blockRoot, signed, postState); err != nil {
		return err
	}

	// 插入block到fork choice store
	if err := s.insertBlockToForkchoiceStore(ctx, signed.Block(), blockRoot, postState); err != nil {
		return errors.Wrapf(err, "could not insert block %d to fork choice store", signed.Block().Slot())
	}
	// 处理block attestations
	if err := s.handleBlockAttestations(ctx, signed.Block(), postState); err != nil {
		return errors.Wrap(err, "could not handle block's attestations")
	}
	// 处理block的BLSToExecutionChanges
	if err := s.handleBlockBLSToExecChanges(signed.Block()); err != nil {
		return errors.Wrap(err, "could not handle block's BLSToExecutionChanges")
	}

	// 插入slashings到fork choice store
	s.InsertSlashingsToForkChoiceStore(ctx, signed.Block().Body().AttesterSlashings())
	if isValidPayload {
		if err := s.cfg.ForkChoiceStore.SetOptimisticToValid(ctx, blockRoot); err != nil {
			// 不能将optimistic block设置为valid
			return errors.Wrap(err, "could not set optimistic block to valid")
		}
	}

	// If slasher is configured, forward the attestations in the block via
	// an event feed for processing.
	// 如果配置了slasher，转发block中的attestations，通过一个event feed，用于处理
	if features.Get().EnableSlasher {
		// Feed the indexed attestation to slasher if enabled. This action
		// is done in the background to avoid adding more load to this critical code path.
		// 提供indexed attestation到slasher，如果它使能的话，这个动作在后台进行，来避免给关键路径
		// 增加额外的负担
		go func() {
			// Using a different context to prevent timeouts as this operation can be expensive
			// and we want to avoid affecting the critical code path.
			ctx := context.TODO()
			for _, att := range signed.Block().Body().Attestations() {
				// 获取committee
				committee, err := helpers.BeaconCommitteeFromState(ctx, preState, att.Data.Slot, att.Data.CommitteeIndex)
				if err != nil {
					log.WithError(err).Error("Could not get attestation committee")
					tracing.AnnotateError(span, err)
					return
				}
				// 转换为indexed attestation
				indexedAtt, err := attestation.ConvertToIndexed(ctx, att, committee)
				if err != nil {
					// 不能转换为indexed attestation
					log.WithError(err).Error("Could not convert to indexed attestation")
					tracing.AnnotateError(span, err)
					return
				}
				// 发送给slasher attestations feed
				s.cfg.SlasherAttestationsFeed.Send(indexedAtt)
			}
		}()
	}

	justified := s.ForkChoicer().JustifiedCheckpoint()
	// 获取justified balances
	balances, err := s.justifiedBalances.get(ctx, justified.Root)
	if err != nil {
		// 读取balance
		msg := fmt.Sprintf("could not read balances for state w/ justified checkpoint %#x", justified.Root)
		return errors.Wrap(err, msg)
	}

	start := time.Now()
	// 更新head
	headRoot, err := s.cfg.ForkChoiceStore.Head(ctx, balances)
	if err != nil {
		log.WithError(err).Warn("Could not update head")
	}
	newBlockHeadElapsedTime.Observe(float64(time.Since(start).Milliseconds()))

	// 通知engine，如果head发生了改变
	if err := s.notifyEngineIfChangedHead(ctx, headRoot); err != nil {
		return err
	}

	// 从pool中移除canonical attestations
	if err := s.pruneCanonicalAttsFromPool(ctx, blockRoot, signed); err != nil {
		return err
	}

	// Send notification of the processed block to the state feed.
	// 发送processed block
	s.cfg.StateNotifier.StateFeed().Send(&feed.Event{
		Type: statefeed.BlockProcessed,
		Data: &statefeed.BlockProcessedData{
			Slot:        signed.Block().Slot(),
			BlockRoot:   blockRoot,
			SignedBlock: signed,
			Verified:    true,
		},
	})

	// Updating next slot state cache can happen in the background. It shouldn't block rest of the process.
	// 更新下一个slot state cache可以在后台发生，它不应该阻塞剩余的处理
	go func() {
		// Use a custom deadline here, since this method runs asynchronously.
		// We ignore the parent method's context and instead create a new one
		// with a custom deadline, therefore using the background context instead.
		slotCtx, cancel := context.WithTimeout(context.Background(), slotDeadline)
		defer cancel()
		if err := transition.UpdateNextSlotCache(slotCtx, blockRoot[:], postState); err != nil {
			// 不能更新下一个
			log.WithError(err).Debug("could not update next slot state cache")
		}
	}()

	// Save justified check point to db.
	// 保存justified checkpoint到db
	postStateJustifiedEpoch := postState.CurrentJustifiedCheckpoint().Epoch
	if justified.Epoch > currStoreJustifiedEpoch || (justified.Epoch == postStateJustifiedEpoch && justified.Epoch > preStateJustifiedEpoch) {
		// 在db中保存justified checkpoint
		if err := s.cfg.BeaconDB.SaveJustifiedCheckpoint(ctx, &ethpb.Checkpoint{
			// 保存justified epoch
			Epoch: justified.Epoch, Root: justified.Root[:],
		}); err != nil {
			return err
		}
	}

	// Save finalized check point to db and more.
	// 保存finalized checkpoint到db以及更多
	postStateFinalizedEpoch := postState.FinalizedCheckpoint().Epoch
	finalized := s.ForkChoicer().FinalizedCheckpoint()
	if finalized.Epoch > currStoreFinalizedEpoch || (finalized.Epoch == postStateFinalizedEpoch && finalized.Epoch > preStateFinalizedEpoch) {
		if err := s.updateFinalized(ctx, &ethpb.Checkpoint{Epoch: finalized.Epoch, Root: finalized.Root[:]}); err != nil {
			return err
		}
		isOptimistic, err := s.cfg.ForkChoiceStore.IsOptimistic(finalized.Root)
		if err != nil {
			return errors.Wrap(err, "could not check if node is optimistically synced")
		}
		go func() {
			// Send an event regarding the new finalized checkpoint over a common event feed.
			// 发送一个event，关于新的finalized checkpoint，通过公共的event feed
			stateRoot := signed.Block().StateRoot()
			s.cfg.StateNotifier.StateFeed().Send(&feed.Event{
				Type: statefeed.FinalizedCheckpoint,
				Data: &ethpbv1.EventFinalizedCheckpoint{
					Epoch:               postState.FinalizedCheckpoint().Epoch,
					Block:               postState.FinalizedCheckpoint().Root,
					State:               stateRoot[:],
					ExecutionOptimistic: isOptimistic,
				},
			})

			// Use a custom deadline here, since this method runs asynchronously.
			// We ignore the parent method's context and instead create a new one
			// with a custom deadline, therefore using the background context instead.
			depCtx, cancel := context.WithTimeout(context.Background(), depositDeadline)
			defer cancel()
			// 不能插入finalized deposits
			if err := s.insertFinalizedDeposits(depCtx, finalized.Root); err != nil {
				log.WithError(err).Error("Could not insert finalized deposits.")
			}
		}()

	}
	defer reportAttestationInclusion(b)
	// 处理epoch boundary
	if err := s.handleEpochBoundary(ctx, postState); err != nil {
		return err
	}
	onBlockProcessingTime.Observe(float64(time.Since(startTime).Milliseconds()))
	return nil
}

func getStateVersionAndPayload(st state.BeaconState) (int, interfaces.ExecutionData, error) {
	if st == nil {
		return 0, nil, errors.New("nil state")
	}
	var preStateHeader interfaces.ExecutionData
	var err error
	preStateVersion := st.Version()
	switch preStateVersion {
	case version.Phase0, version.Altair:
	default:
		// 获取最新的execution payload header，只在Phase0和Altair之后有效
		preStateHeader, err = st.LatestExecutionPayloadHeader()
		if err != nil {
			return 0, nil, err
		}
	}
	return preStateVersion, preStateHeader, nil
}

func (s *Service) onBlockBatch(ctx context.Context, blks []interfaces.SignedBeaconBlock,
	blockRoots [][32]byte) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.onBlockBatch")
	defer span.End()

	if len(blks) == 0 || len(blockRoots) == 0 {
		return errors.New("no blocks provided")
	}

	if len(blks) != len(blockRoots) {
		return errWrongBlockCount
	}

	if err := consensusblocks.BeaconBlockIsNil(blks[0]); err != nil {
		return invalidBlock{error: err}
	}
	b := blks[0].Block()

	// Retrieve incoming block's pre state.
	// 获取到来的block的prestate
	if err := s.verifyBlkPreState(ctx, b); err != nil {
		return err
	}
	preState, err := s.cfg.StateGen.StateByRootInitialSync(ctx, b.ParentRoot())
	if err != nil {
		return err
	}
	if preState == nil || preState.IsNil() {
		return fmt.Errorf("nil pre state for slot %d", b.Slot())
	}

	// Fill in missing blocks
	// 填充fork choice中丢失的blocks
	if err := s.fillInForkChoiceMissingBlocks(ctx, blks[0].Block(), preState.CurrentJustifiedCheckpoint(), preState.FinalizedCheckpoint()); err != nil {
		return errors.Wrap(err, "could not fill in missing blocks to forkchoice")
	}

	jCheckpoints := make([]*ethpb.Checkpoint, len(blks))
	fCheckpoints := make([]*ethpb.Checkpoint, len(blks))
	sigSet := &bls.SignatureBatch{
		Signatures: [][]byte{},
		PublicKeys: []bls.PublicKey{},
		Messages:   [][32]byte{},
	}
	type versionAndHeader struct {
		version int
		header  interfaces.ExecutionData
	}
	preVersionAndHeaders := make([]*versionAndHeader, len(blks))
	postVersionAndHeaders := make([]*versionAndHeader, len(blks))
	var set *bls.SignatureBatch
	boundaries := make(map[[32]byte]state.BeaconState)
	// 遍历blocks
	for i, b := range blks {
		// 获取state version以及payload
		v, h, err := getStateVersionAndPayload(preState)
		if err != nil {
			return err
		}
		preVersionAndHeaders[i] = &versionAndHeader{
			version: v,
			header:  h,
		}

		// 返回的第一个值是signatureBatch
		set, preState, err = transition.ExecuteStateTransitionNoVerifyAnySig(ctx, preState, b)
		if err != nil {
			return invalidBlock{error: err}
		}
		// Save potential boundary states.
		// 保存临时的boundary state
		if slots.IsEpochStart(preState.Slot()) {
			boundaries[blockRoots[i]] = preState.Copy()
		}
		jCheckpoints[i] = preState.CurrentJustifiedCheckpoint()
		fCheckpoints[i] = preState.FinalizedCheckpoint()

		// 获取state version以及payload
		v, h, err = getStateVersionAndPayload(preState)
		if err != nil {
			return err
		}
		postVersionAndHeaders[i] = &versionAndHeader{
			version: v,
			header:  h,
		}
		sigSet.Join(set)
	}
	verify, err := sigSet.Verify()
	if err != nil {
		return invalidBlock{error: err}
	}
	if !verify {
		// batch block signature校验失败
		return errors.New("batch block signature verification failed")
	}

	// blocks have been verified, save them and call the engine
	// blocks已经校验完成了，保存它们并且调用引擎
	pendingNodes := make([]*forkchoicetypes.BlockAndCheckpoints, len(blks))
	var isValidPayload bool
	for i, b := range blks {
		// 通知有新的payload
		isValidPayload, err = s.notifyNewPayload(ctx,
			postVersionAndHeaders[i].version,
			postVersionAndHeaders[i].header, b)
		if err != nil {
			return err
		}
		if isValidPayload {
			if err := s.validateMergeTransitionBlock(ctx, preVersionAndHeaders[i].version,
				preVersionAndHeaders[i].header, b); err != nil {
				return err
			}
		}
		args := &forkchoicetypes.BlockAndCheckpoints{Block: b.Block(),
			JustifiedCheckpoint: jCheckpoints[i],
			FinalizedCheckpoint: fCheckpoints[i]}
		pendingNodes[len(blks)-i-1] = args
		if err := s.saveInitSyncBlock(ctx, blockRoots[i], b); err != nil {
			tracing.AnnotateError(span, err)
			return err
		}
		if err := s.cfg.BeaconDB.SaveStateSummary(ctx, &ethpb.StateSummary{
			Slot: b.Block().Slot(),
			Root: blockRoots[i][:],
		}); err != nil {
			tracing.AnnotateError(span, err)
			return err
		}
		if i > 0 && jCheckpoints[i].Epoch > jCheckpoints[i-1].Epoch {
			// 保存justified checkpoint到db中
			if err := s.cfg.BeaconDB.SaveJustifiedCheckpoint(ctx, jCheckpoints[i]); err != nil {
				tracing.AnnotateError(span, err)
				return err
			}
		}
		if i > 0 && fCheckpoints[i].Epoch > fCheckpoints[i-1].Epoch {
			// 更新finalized
			if err := s.updateFinalized(ctx, fCheckpoints[i]); err != nil {
				tracing.AnnotateError(span, err)
				return err
			}
		}
	}
	// Insert all nodes but the last one to forkchoice
	// 插入所有的nodes，除了最后一个到forkchoice
	if err := s.cfg.ForkChoiceStore.InsertOptimisticChain(ctx, pendingNodes); err != nil {
		return errors.Wrap(err, "could not insert batch to forkchoice")
	}
	// Insert the last block to forkchoice
	// 插入最后一个block到forkchoice
	lastBR := blockRoots[len(blks)-1]
	if err := s.cfg.ForkChoiceStore.InsertNode(ctx, preState, lastBR); err != nil {
		return errors.Wrap(err, "could not insert last block in batch to forkchoice")
	}
	// Set their optimistic status
	// 设置它们的optimistic status
	if isValidPayload {
		if err := s.cfg.ForkChoiceStore.SetOptimisticToValid(ctx, lastBR); err != nil {
			return errors.Wrap(err, "could not set optimistic block to valid")
		}
	}

	for r, st := range boundaries {
		if err := s.cfg.StateGen.SaveState(ctx, r, st); err != nil {
			return err
		}
	}
	// Also saves the last post state which to be used as pre state for the next batch.
	// 同时保存最后的post state，它可以用作下一个batch的pre state
	lastB := blks[len(blks)-1]
	if err := s.cfg.StateGen.SaveState(ctx, lastBR, preState); err != nil {
		return err
	}
	arg := &notifyForkchoiceUpdateArg{
		headState: preState,
		headRoot:  lastBR,
		headBlock: lastB.Block(),
	}
	if _, err := s.notifyForkchoiceUpdate(ctx, arg); err != nil {
		return err
	}
	return s.saveHeadNoDB(ctx, lastB, lastBR, preState)
}

// Epoch boundary bookkeeping such as logging epoch summaries.
// Epoch boundary bookkeeping，例如记录epoch summaries
func (s *Service) handleEpochBoundary(ctx context.Context, postState state.BeaconState) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.handleEpochBoundary")
	defer span.End()

	if postState.Slot()+1 == s.nextEpochBoundarySlot {
		// 如果下一个slot就是下一个epoch了
		copied := postState.Copy()
		// 对slot进行处理
		copied, err := transition.ProcessSlots(ctx, copied, copied.Slot()+1)
		if err != nil {
			return err
		}
		// Update caches for the next epoch at epoch boundary slot - 1.
		// 为下一个epoch更新caches，在epoch boundary slot - 1
		if err := helpers.UpdateCommitteeCache(ctx, copied, coreTime.CurrentEpoch(copied)); err != nil {
			return err
		}
		// 更新proposer indices
		if err := helpers.UpdateProposerIndicesInCache(ctx, copied); err != nil {
			return err
		}
	} else if postState.Slot() >= s.nextEpochBoundarySlot {
		s.headLock.RLock()
		st := s.head.state
		s.headLock.RUnlock()
		if err := reportEpochMetrics(ctx, postState, st); err != nil {
			return err
		}

		var err error
		// 获取下一个epoch boundary
		s.nextEpochBoundarySlot, err = slots.EpochStart(coreTime.NextEpoch(postState))
		if err != nil {
			return err
		}

		// Update caches at epoch boundary slot.
		// The following updates have short cut to return nil cheaply if fulfilled during boundary slot - 1.
		// 更新epoch boundary slot的缓存，下面的更新有short cut去返回nil，如果在boundary slot - 1进行了填充
		if err := helpers.UpdateCommitteeCache(ctx, postState, coreTime.CurrentEpoch(postState)); err != nil {
			return err
		}
		if err := helpers.UpdateProposerIndicesInCache(ctx, postState); err != nil {
			return err
		}
	}

	return nil
}

// This feeds in the block to fork choice store. It's allows fork choice store
// to gain information on the most current chain.
// 填入block到fork choice store，它允许fork choice store获取当前最新的chain的信息
func (s *Service) insertBlockToForkchoiceStore(ctx context.Context, blk interfaces.BeaconBlock, root [32]byte, st state.BeaconState) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.insertBlockToForkchoiceStore")
	defer span.End()

	if !s.cfg.ForkChoiceStore.HasNode(blk.ParentRoot()) {
		// 如果parent block在fork choice store中不存在
		fCheckpoint := st.FinalizedCheckpoint()
		jCheckpoint := st.CurrentJustifiedCheckpoint()
		if err := s.fillInForkChoiceMissingBlocks(ctx, blk, fCheckpoint, jCheckpoint); err != nil {
			return err
		}
	}

	// 直接插入node
	if err := s.cfg.ForkChoiceStore.InsertNode(ctx, st, root); err != nil {
		return err
	}

	return nil
}

// This feeds in the attestations included in the block to fork choice store. It's allows fork choice store
// to gain information on the most current chain.
// 将block中包含的attestations填充到fork choice store，它允许fork choice store获取最近的chain的信息
func (s *Service) handleBlockAttestations(ctx context.Context, blk interfaces.BeaconBlock, st state.BeaconState) error {
	// Feed in block's attestations to fork choice store.
	// 填充block的attestations到fork choice store
	for _, a := range blk.Body().Attestations() {
		// 基于attestation中的数据获取committee
		committee, err := helpers.BeaconCommitteeFromState(ctx, st, a.Data.Slot, a.Data.CommitteeIndex)
		if err != nil {
			return err
		}
		// 基于aggregationBits和committee获取索引
		indices, err := attestation.AttestingIndices(a.AggregationBits, committee)
		if err != nil {
			return err
		}
		r := bytesutil.ToBytes32(a.Data.BeaconBlockRoot)
		if s.cfg.ForkChoiceStore.HasNode(r) {
			// 如果block包含在fork choice store中的话，让fork choice处理attestations
			s.cfg.ForkChoiceStore.ProcessAttestation(ctx, indices, r, a.Data.Target.Epoch)
		} else if err := s.cfg.AttPool.SaveBlockAttestation(a); err != nil {
			// 否则，插入block attestation到att pool
			return err
		}
	}
	return nil
}

func (s *Service) handleBlockBLSToExecChanges(blk interfaces.BeaconBlock) error {
	if blk.Version() < version.Capella {
		return nil
	}
	changes, err := blk.Body().BLSToExecutionChanges()
	if err != nil {
		return errors.Wrap(err, "could not get BLSToExecutionChanges")
	}
	for _, change := range changes {
		// 将对应的BLSToExecutionChange标记为included
		if err := s.cfg.BLSToExecPool.MarkIncluded(change); err != nil {
			return errors.Wrap(err, "could not mark BLSToExecutionChange as included")
		}
	}
	return nil
}

// InsertSlashingsToForkChoiceStore inserts attester slashing indices to fork choice store.
// InsertSlashingsToForkChoiceStore插入attester slashing indices到fork choice store
// To call this function, it's caller's responsibility to ensure the slashing object is valid.
// 调用者要确保slashing对象是正确的
func (s *Service) InsertSlashingsToForkChoiceStore(ctx context.Context, slashings []*ethpb.AttesterSlashing) {
	for _, slashing := range slashings {
		indices := blocks.SlashableAttesterIndices(slashing)
		for _, index := range indices {
			// 插入fork choice store
			s.ForkChoicer().InsertSlashedIndex(ctx, types.ValidatorIndex(index))
		}
	}
}

// This saves post state info to DB or cache. This also saves post state info to fork choice store.
// Post state info consists of processed block and state. Do not call this method unless the block and state are verified.
// 保存post state到DB或者cache中，它同时保存post state info到fork choice store
// post state由processed block和state组成，不要调用这个方法，除非block和state被校验
func (s *Service) savePostStateInfo(ctx context.Context, r [32]byte, b interfaces.SignedBeaconBlock, st state.BeaconState) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.savePostStateInfo")
	defer span.End()
	if err := s.cfg.BeaconDB.SaveBlock(ctx, b); err != nil {
		return errors.Wrapf(err, "could not save block from slot %d", b.Block().Slot())
	}
	if err := s.cfg.StateGen.SaveState(ctx, r, st); err != nil {
		return errors.Wrap(err, "could not save state")
	}
	return nil
}

// This removes the attestations from the mem pool. It will only remove the attestations if input root `r` is canonical,
// meaning the block `b` is part of the canonical chain.
// 将attestations从mem pool中移除，它只会移除attestations，如果输入的root `r`是canonical
// 意味着block `b`是canonical chain的一部分
func (s *Service) pruneCanonicalAttsFromPool(ctx context.Context, r [32]byte, b interfaces.SignedBeaconBlock) error {
	canonical, err := s.IsCanonical(ctx, r)
	if err != nil {
		return err
	}
	if !canonical {
		return nil
	}

	atts := b.Block().Body().Attestations()
	for _, att := range atts {
		// 移除aggregated或者unaggregated attestations
		if helpers.IsAggregated(att) {
			if err := s.cfg.AttPool.DeleteAggregatedAttestation(att); err != nil {
				return err
			}
		} else {
			if err := s.cfg.AttPool.DeleteUnaggregatedAttestation(att); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateMergeTransitionBlock validates the merge transition block.
func (s *Service) validateMergeTransitionBlock(ctx context.Context, stateVersion int, stateHeader interfaces.ExecutionData, blk interfaces.SignedBeaconBlock) error {
	// Skip validation if block is older than Bellatrix.
	if blocks.IsPreBellatrixVersion(blk.Block().Version()) {
		return nil
	}

	// Skip validation if block has an empty payload.
	payload, err := blk.Block().Body().Execution()
	if err != nil {
		return invalidBlock{error: err}
	}
	isEmpty, err := consensusblocks.IsEmptyExecutionData(payload)
	if err != nil {
		return err
	}
	if isEmpty {
		return nil
	}

	// Handle case where pre-state is Altair but block contains payload.
	// To reach here, the block must have contained a valid payload.
	if blocks.IsPreBellatrixVersion(stateVersion) {
		return s.validateMergeBlock(ctx, blk)
	}

	// Skip validation if the block is not a merge transition block.
	// To reach here. The payload must be non-empty. If the state header is empty then it's at transition.
	empty, err := consensusblocks.IsEmptyExecutionData(stateHeader)
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}
	return s.validateMergeBlock(ctx, blk)
}

// This routine checks if there is a cached proposer payload ID available for the next slot proposer.
// 这个routine检查是否有一个cached proposer payload ID可以用于下一个slot proposer
// If there is not, it will call forkchoice updated with the correct payload attribute then cache the payload ID.
// 如果没有，它会调用forkchoice，用正确的payload attribute更新，之后缓存payload ID
func (s *Service) fillMissingPayloadIDRoutine(ctx context.Context, stateFeed *event.Feed) {
	// Wait for state to be initialized.
	// 等待state初始化完成
	stateChannel := make(chan *feed.Event, 1)
	stateSub := stateFeed.Subscribe(stateChannel)
	go func() {
		select {
		case <-s.ctx.Done():
			stateSub.Unsubscribe()
			return
		case <-stateChannel:
			stateSub.Unsubscribe()
			break
		}

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case ti := <-ticker.C:
				if err := s.fillMissingBlockPayloadId(ctx, ti); err != nil {
					// 不能填充缺失的payload ID
					log.WithError(err).Error("Could not fill missing payload ID")
				}
			case <-s.ctx.Done():
				log.Debug("Context closed, exiting routine")
				return
			}
		}
	}()
}

// Returns true if time `t` is halfway through the slot in sec.
// 返回true，如果time `t`是slot，在sec的一半
func atHalfSlot(t time.Time) bool {
	s := params.BeaconConfig().SecondsPerSlot
	return uint64(t.Second())%s == s/2
}

func (s *Service) fillMissingBlockPayloadId(ctx context.Context, ti time.Time) error {
	if !atHalfSlot(ti) {
		return nil
	}
	// 如果当前的slot是接收到的最高的block slot，则返回
	if s.CurrentSlot() == s.cfg.ForkChoiceStore.HighestReceivedBlockSlot() {
		return nil
	}
	// Head root should be empty when retrieving proposer index for the next slot.
	// Head root应该为空，当为下一个slot获取proposer index
	_, id, has := s.cfg.ProposerSlotIndexCache.GetProposerPayloadIDs(s.CurrentSlot()+1, [32]byte{} /* head root */)
	// There exists proposer for next slot, but we haven't called fcu w/ payload attribute yet.
	// 对于下一个slot存在一个proposer，但是我们还没有调用payload attribute
	if has && id == [8]byte{} {
		missedPayloadIDFilledCount.Inc()
		headBlock, err := s.headBlock()
		if err != nil {
			return err
		} else {
			if _, err := s.notifyForkchoiceUpdate(ctx, &notifyForkchoiceUpdateArg{
				// 获取head state，head block以及head root，
				headState: s.headState(ctx),
				headRoot:  s.headRoot(),
				headBlock: headBlock.Block(),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
