package blockchain

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed"
	statefeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/state"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	"github.com/prysmaticlabs/prysm/v3/monitoring/tracing"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/runtime/version"
	"github.com/prysmaticlabs/prysm/v3/time"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"go.opencensus.io/trace"
)

// This defines how many epochs since finality the run time will begin to save hot state on to the DB.
// 定义了从finality之后多少个epochs，runtime会开始保存hot state到DB中
var epochsSinceFinalitySaveHotStateDB = primitives.Epoch(100)

// BlockReceiver interface defines the methods of chain service for receiving and processing new blocks.
// BlockReceiver接口定义了chain service的方法，用于获取以及处理新的blocks
type BlockReceiver interface {
	ReceiveBlock(ctx context.Context, block interfaces.ReadOnlySignedBeaconBlock, blockRoot [32]byte) error
	ReceiveBlockBatch(ctx context.Context, blocks []interfaces.ReadOnlySignedBeaconBlock, blkRoots [][32]byte) error
	HasBlock(ctx context.Context, root [32]byte) bool
}

// SlashingReceiver interface defines the methods of chain service for receiving validated slashing over the wire.
type SlashingReceiver interface {
	ReceiveAttesterSlashing(ctx context.Context, slashings *ethpb.AttesterSlashing)
}

// ReceiveBlock is a function that defines the operations (minus pubsub)
// that are performed on a received block. The operations consist of:
//  1. Validate block, apply state transition and update checkpoints
//  2. Apply fork choice to the processed block
//  3. Save latest head info
//
// ReceiveBlock是一个函数定义了如下操作（除了pubsub），对一个接收到的block
// 1. 检查block，应用state transition以及更新checkpoints
// 2. 对处理的block执行fork choice
// 3. 保存最新的head info
func (s *Service) ReceiveBlock(ctx context.Context, block interfaces.ReadOnlySignedBeaconBlock, blockRoot [32]byte) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.ReceiveBlock")
	defer span.End()
	receivedTime := time.Now()
	blockCopy, err := block.Copy()
	if err != nil {
		return err
	}

	s.cfg.ForkChoiceStore.Lock()
	defer s.cfg.ForkChoiceStore.Unlock()

	// Apply state transition on the new block.
	// 对新的block执行state transition
	if err := s.onBlock(ctx, blockCopy, blockRoot); err != nil {
		err := errors.Wrap(err, "could not process block")
		tracing.AnnotateError(span, err)
		return err
	}

	// Handle post block operations such as attestations and exits.
	// 处理post block操作，例如attestation以及exits
	if err := s.handlePostBlockOperations(blockCopy.Block()); err != nil {
		return err
	}

	// Have we been finalizing? Should we start saving hot states to db?
	// 我们已经finalizing了？我们应该开始保存hot states到db?
	if err := s.checkSaveHotStateDB(ctx); err != nil {
		return err
	}

	// Reports on block and fork choice metrics.
	// 报告block以及fork choice metrics
	cp := s.ForkChoicer().FinalizedCheckpoint()
	finalized := &ethpb.Checkpoint{Epoch: cp.Epoch, Root: bytesutil.SafeCopyBytes(cp.Root[:])}
	reportSlotMetrics(blockCopy.Block().Slot(), s.HeadSlot(), s.CurrentSlot(), finalized)

	// Log block sync status.
	// 对log sync status进行日志
	cp = s.ForkChoicer().JustifiedCheckpoint()
	justified := &ethpb.Checkpoint{Epoch: cp.Epoch, Root: bytesutil.SafeCopyBytes(cp.Root[:])}
	if err := logBlockSyncStatus(blockCopy.Block(), blockRoot, justified, finalized, receivedTime, uint64(s.genesisTime.Unix())); err != nil {
		log.WithError(err).Error("Unable to log block sync status")
	}
	// Log payload data
	// 对payload data进行日志
	if err := logPayload(blockCopy.Block()); err != nil {
		log.WithError(err).Error("Unable to log debug block payload data")
	}
	// Log state transition data.
	// 对state transition data进行日志
	if err := logStateTransitionData(blockCopy.Block()); err != nil {
		log.WithError(err).Error("Unable to log state transition data")
	}

	return nil
}

// ReceiveBlockBatch processes the whole block batch at once, assuming the block batch is linear ,transitioning
// the state, performing batch verification of all collected signatures and then performing the appropriate
// actions for a block post-transition.
// ReceiveBlockBatch一次性处理整个的block batch，假设block batch是线性的，转换state，执行批量校验，对于所有收集的sinature
// 之后执行适当的actions，对于block post-transition
func (s *Service) ReceiveBlockBatch(ctx context.Context, blocks []interfaces.ReadOnlySignedBeaconBlock, blkRoots [][32]byte) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.ReceiveBlockBatch")
	defer span.End()

	s.cfg.ForkChoiceStore.Lock()
	defer s.cfg.ForkChoiceStore.Unlock()

	// Apply state transition on the incoming newly received block batches, one by one.
	// 应用state transition，对于批量接收的block，一个接一个
	if err := s.onBlockBatch(ctx, blocks, blkRoots); err != nil {
		err := errors.Wrap(err, "could not process block in batch")
		tracing.AnnotateError(span, err)
		return err
	}

	for i, b := range blocks {
		blockCopy, err := b.Copy()
		if err != nil {
			return err
		}
		// Send notification of the processed block to the state feed.
		// 发送处理的block到state feed
		s.cfg.StateNotifier.StateFeed().Send(&feed.Event{
			Type: statefeed.BlockProcessed,
			Data: &statefeed.BlockProcessedData{
				Slot:        blockCopy.Block().Slot(),
				BlockRoot:   blkRoots[i],
				SignedBlock: blockCopy,
				Verified:    true,
			},
		})

		// Reports on blockCopy and fork choice metrics.
		// 汇报blockCopy以及fork choice metrics
		cp := s.ForkChoicer().FinalizedCheckpoint()
		finalized := &ethpb.Checkpoint{Epoch: cp.Epoch, Root: bytesutil.SafeCopyBytes(cp.Root[:])}
		reportSlotMetrics(blockCopy.Block().Slot(), s.HeadSlot(), s.CurrentSlot(), finalized)
	}

	// 保存blocks到db中
	if err := s.cfg.BeaconDB.SaveBlocks(ctx, s.getInitSyncBlocks()); err != nil {
		return err
	}
	finalized := s.ForkChoicer().FinalizedCheckpoint()
	if finalized == nil {
		return errNilFinalizedInStore
	}
	if err := s.wsVerifier.VerifyWeakSubjectivity(s.ctx, finalized.Epoch); err != nil {
		// log.Fatalf will prevent defer from being called
		span.End()
		// Exit run time if the node failed to verify weak subjectivity checkpoint.
		log.WithError(err).Fatal("Could not verify weak subjectivity checkpoint")
	}

	return nil
}

// HasBlock returns true if the block of the input root exists in initial sync blocks cache or DB.
func (s *Service) HasBlock(ctx context.Context, root [32]byte) bool {
	return s.hasBlockInInitSyncOrDB(ctx, root)
}

// ReceiveAttesterSlashing receives an attester slashing and inserts it to forkchoice
func (s *Service) ReceiveAttesterSlashing(ctx context.Context, slashing *ethpb.AttesterSlashing) {
	s.ForkChoicer().Lock()
	defer s.ForkChoicer().Unlock()
	s.InsertSlashingsToForkChoiceStore(ctx, []*ethpb.AttesterSlashing{slashing})
}

func (s *Service) handlePostBlockOperations(b interfaces.ReadOnlyBeaconBlock) error {
	// Mark block exits as seen so we don't include same ones in future blocks.
	for _, e := range b.Body().VoluntaryExits() {
		s.cfg.ExitPool.MarkIncluded(e)
	}

	// Mark block BLS changes as seen so we don't include same ones in future blocks.
	if err := s.handleBlockBLSToExecChanges(b); err != nil {
		return errors.Wrap(err, "could not process BLSToExecutionChanges")
	}

	//  Mark attester slashings as seen so we don't include same ones in future blocks.
	for _, as := range b.Body().AttesterSlashings() {
		s.cfg.SlashingPool.MarkIncludedAttesterSlashing(as)
	}
	return nil
}

func (s *Service) handleBlockBLSToExecChanges(blk interfaces.ReadOnlyBeaconBlock) error {
	if blk.Version() < version.Capella {
		return nil
	}
	changes, err := blk.Body().BLSToExecutionChanges()
	if err != nil {
		return errors.Wrap(err, "could not get BLSToExecutionChanges")
	}
	for _, change := range changes {
		s.cfg.BLSToExecPool.MarkIncluded(change)
	}
	return nil
}

// This checks whether it's time to start saving hot state to DB.
// 检查是否是时候开始保存hot state到DB中
// It's time when there's `epochsSinceFinalitySaveHotStateDB` epochs of non-finality.
// Requires a read lock on forkchoice
// 当从non-finality开始有`epochsSinceFinalitySaveHotStateDB`时开始，需要对于forkchoice的读锁
func (s *Service) checkSaveHotStateDB(ctx context.Context) error {
	currentEpoch := slots.ToEpoch(s.CurrentSlot())
	// Prevent `sinceFinality` going underflow.
	var sinceFinality primitives.Epoch
	finalized := s.ForkChoicer().FinalizedCheckpoint()
	if finalized == nil {
		return errNilFinalizedInStore
	}
	// 当前的epoch，大于finalized epoch
	if currentEpoch > finalized.Epoch {
		sinceFinality = currentEpoch - finalized.Epoch
	}

	// 超过了epochsSinceFinalitySaveHotStateDB
	if sinceFinality >= epochsSinceFinalitySaveHotStateDB {
		s.cfg.StateGen.EnableSaveHotStateToDB(ctx)
		return nil
	}

	// 否则清理SaveHotStateToDB
	return s.cfg.StateGen.DisableSaveHotStateToDB(ctx)
}
