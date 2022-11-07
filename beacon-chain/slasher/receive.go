package slasher

import (
	"context"
	"time"

	"github.com/pkg/errors"
	slashertypes "github.com/prysmaticlabs/prysm/beacon-chain/slasher/types"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/time/slots"
	"github.com/sirupsen/logrus"
)

// Receive indexed attestations from some source event feed,
// 从source event feed接收到indexed attestations
// validating their integrity before appending them to an attestation queue
// for batch processing in a separate routine.
// 校验它们的完整性，在扩展它们到一个attestation queue之前，用于在另一个routine进行批量处理
func (s *Service) receiveAttestations(ctx context.Context, indexedAttsChan chan *ethpb.IndexedAttestation) {
	// 订阅attestations
	sub := s.serviceCfg.IndexedAttestationsFeed.Subscribe(indexedAttsChan)
	defer sub.Unsubscribe()
	for {
		select {
		case att := <-indexedAttsChan:
			if !validateAttestationIntegrity(att) {
				continue
			}
			signingRoot, err := att.Data.HashTreeRoot()
			if err != nil {
				// 不能获取attestation的hash root
				log.WithError(err).Error("Could not get hash tree root of attestation")
				continue
			}
			attWrapper := &slashertypes.IndexedAttestationWrapper{
				IndexedAttestation: att,
				SigningRoot:        signingRoot,
			}
			// 加入到atts queue
			s.attsQueue.push(attWrapper)
		case err := <-sub.Err():
			log.WithError(err).Debug("Subscriber closed with error")
			return
		case <-ctx.Done():
			return
		}
	}
}

// Receive beacon blocks from some source event feed,
// 从source event feed中接收beacon blocks
func (s *Service) receiveBlocks(ctx context.Context, beaconBlockHeadersChan chan *ethpb.SignedBeaconBlockHeader) {
	// 订阅block header
	sub := s.serviceCfg.BeaconBlockHeadersFeed.Subscribe(beaconBlockHeadersChan)
	defer sub.Unsubscribe()
	for {
		select {
		case blockHeader := <-beaconBlockHeadersChan:
			if !validateBlockHeaderIntegrity(blockHeader) {
				continue
			}
			signingRoot, err := blockHeader.Header.HashTreeRoot()
			if err != nil {
				log.WithError(err).Error("Could not get hash tree root of signed block header")
				continue
			}
			wrappedProposal := &slashertypes.SignedBlockHeaderWrapper{
				SignedBeaconBlockHeader: blockHeader,
				SigningRoot:             signingRoot,
			}
			// 加入队列中
			s.blksQueue.push(wrappedProposal)
		case err := <-sub.Err():
			log.WithError(err).Debug("Subscriber closed with error")
			return
		case <-ctx.Done():
			return
		}
	}
}

// Process queued attestations every time a slot ticker fires. We retrieve
// these attestations from a queue, then group them all by validator chunk index.
// This grouping will allow us to perform detection on batches of attestations
// per validator chunk index which can be done concurrently.
// 处理排队的attestations，每次一个slot ticker触发的时候，我们从一个队列获取attestations，之后将它们
// 通过validator chunk index聚合，这个grouping会让我们批量执行detection，关于每个validator
// chunk index，它们可以并行执行
func (s *Service) processQueuedAttestations(ctx context.Context, slotTicker <-chan types.Slot) {
	for {
		select {
		case currentSlot := <-slotTicker:
			attestations := s.attsQueue.dequeue()
			currentEpoch := slots.ToEpoch(currentSlot)
			// We take all the attestations in the queue and filter out
			// those which are valid now and valid in the future.
			// 我们获取队列中的所有attestations并且过滤那些现在合法的以及未来合法的
			validAtts, validInFuture, numDropped := s.filterAttestations(attestations, currentEpoch)

			deferredAttestationsTotal.Add(float64(len(validInFuture)))
			droppedAttestationsTotal.Add(float64(numDropped))

			// We add back those attestations that are valid in the future to the queue.
			// 我们将这些合法的attestations添加到队列中
			s.attsQueue.extend(validInFuture)

			log.WithFields(logrus.Fields{
				"currentSlot":     currentSlot,
				"currentEpoch":    currentEpoch,
				"numValidAtts":    len(validAtts),
				"numDeferredAtts": len(validInFuture),
				"numDroppedAtts":  numDropped,
			}).Info("Processing queued attestations for slashing detection")

			// Save the attestation records to our database.
			// 保存attestation records到数据库中
			if err := s.serviceCfg.Database.SaveAttestationRecordsForValidators(
				ctx, validAtts,
			); err != nil {
				log.WithError(err).Error("Could not save attestation records to DB")
				continue
			}

			// Check for slashings.
			// 检查slashings
			slashings, err := s.checkSlashableAttestations(ctx, currentEpoch, validAtts)
			if err != nil {
				log.WithError(err).Error("Could not check slashable attestations")
				continue
			}

			// Process attester slashings by verifying their signatures, submitting
			// to the beacon node's operations pool, and logging them.
			// 处理attester slashings，通过校验它们的signatures，提交给beacon node的operations pool
			// 并且日志记录
			if err := s.processAttesterSlashings(ctx, slashings); err != nil {
				log.WithError(err).Error("Could not process attester slashings")
				continue
			}

			processedAttestationsTotal.Add(float64(len(validAtts)))
		case <-ctx.Done():
			return
		}
	}
}

// Process queued blocks every time an epoch ticker fires. We retrieve
// these blocks from a queue, then perform double proposal detection.
// 每次在一个epoch ticker触发的时候处理queued blocks，我们从一个队列中获取blocks
// 之后再进行double proposal detection
func (s *Service) processQueuedBlocks(ctx context.Context, slotTicker <-chan types.Slot) {
	for {
		select {
		case currentSlot := <-slotTicker:
			blocks := s.blksQueue.dequeue()
			currentEpoch := slots.ToEpoch(currentSlot)

			receivedBlocksTotal.Add(float64(len(blocks)))

			log.WithFields(logrus.Fields{
				"currentSlot":  currentSlot,
				"currentEpoch": currentEpoch,
				"numBlocks":    len(blocks),
			}).Info("Processing queued blocks for slashing detection")

			start := time.Now()
			// Check for slashings.
			// 检测slashings
			slashings, err := s.detectProposerSlashings(ctx, blocks)
			if err != nil {
				log.WithError(err).Error("Could not detect proposer slashings")
				continue
			}

			// Process proposer slashings by verifying their signatures, submitting
			// to the beacon node's operations pool, and logging them.
			// 通过校验它们的signatures来处理proposer slashing，提交它们到beacon node的operations pool
			// 并且记录它们
			if err := s.processProposerSlashings(ctx, slashings); err != nil {
				log.WithError(err).Error("Could not process proposer slashings")
				continue
			}

			log.WithField("elapsed", time.Since(start)).Debug("Done checking slashable blocks")

			processedBlocksTotal.Add(float64(len(blocks)))
		case <-ctx.Done():
			return
		}
	}
}

// Prunes slasher data on each slot tick to prevent unnecessary build-up of disk space usage.
// 清理每个slot中的slasher data，来防止不必要的磁盘使用空间的累计
func (s *Service) pruneSlasherData(ctx context.Context, slotTicker <-chan types.Slot) {
	for {
		select {
		case <-slotTicker:
			headEpoch := slots.ToEpoch(s.serviceCfg.HeadStateFetcher.HeadSlot())
			if err := s.pruneSlasherDataWithinSlidingWindow(ctx, headEpoch); err != nil {
				log.WithError(err).Error("Could not prune slasher data")
				continue
			}
		case <-ctx.Done():
			return
		}
	}
}

// Prunes slasher data by using a sliding window of [current_epoch - HISTORY_LENGTH, current_epoch].
// All data before that window is unnecessary for slasher, so can be periodically deleted.
// Say HISTORY_LENGTH is 4 and we have data for epochs 0, 1, 2, 3. Once we hit epoch 4, the sliding window
// we care about is 1, 2, 3, 4, so we can delete data for epoch 0.
func (s *Service) pruneSlasherDataWithinSlidingWindow(ctx context.Context, currentEpoch types.Epoch) error {
	var maxPruningEpoch types.Epoch
	if currentEpoch >= s.params.historyLength {
		maxPruningEpoch = currentEpoch - s.params.historyLength
	} else {
		// If the current epoch is less than the history length, we should not
		// attempt to prune at all.
		return nil
	}
	start := time.Now()
	log.WithFields(logrus.Fields{
		"currentEpoch":          currentEpoch,
		"pruningAllBeforeEpoch": maxPruningEpoch,
	}).Info("Pruning old attestations and proposals for slasher")
	numPrunedAtts, err := s.serviceCfg.Database.PruneAttestationsAtEpoch(
		ctx, maxPruningEpoch,
	)
	if err != nil {
		return errors.Wrap(err, "Could not prune attestations")
	}
	numPrunedProposals, err := s.serviceCfg.Database.PruneProposalsAtEpoch(
		ctx, maxPruningEpoch,
	)
	if err != nil {
		return errors.Wrap(err, "Could not prune proposals")
	}
	fields := logrus.Fields{}
	if numPrunedAtts > 0 {
		fields["numPrunedAtts"] = numPrunedAtts
	}
	if numPrunedProposals > 0 {
		fields["numPrunedProposals"] = numPrunedProposals
	}
	fields["elapsed"] = time.Since(start)
	log.WithFields(fields).Info("Done pruning old attestations and proposals for slasher")
	return nil
}
