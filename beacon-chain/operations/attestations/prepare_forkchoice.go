package attestations

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/prysmaticlabs/go-bitfield"
	"github.com/prysmaticlabs/prysm/v3/crypto/hash"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	attaggregation "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1/attestation/aggregation/attestations"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"go.opencensus.io/trace"
)

// Prepare attestations for fork choice three times per slot.
// 对于每个slot，准备attestations，对于fork choice，三次
var prepareForkChoiceAttsPeriod = slots.DivideSlotBy(3 /* times-per-slot */)

// This prepares fork choice attestations by running batchForkChoiceAtts
// every prepareForkChoiceAttsPeriod.
// 这个函数通过运行batchForkChoiceAtts准备fork choice attestations，每个prepareForkChoiceAttsPeriod
func (s *Service) prepareForkChoiceAtts() {
	ticker := time.NewTicker(prepareForkChoiceAttsPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			// 定时进行batch
			if err := s.batchForkChoiceAtts(s.ctx); err != nil {
				// 不能为fork choice准备attestations
				log.WithError(err).Error("Could not prepare attestations for fork choice")
			}
		case <-s.ctx.Done():
			log.Debug("Context closed, exiting routine")
			return
		}
	}
}

// This gets the attestations from the unaggregated, aggregated and block
// pool. Then finds the common data, aggregate and batch them for fork choice.
// The resulting attestations are saved in the fork choice pool.
// 这个函数从unaggregated, aggregated以及block pool中获取attestations，找到common data
// 聚合并且batch它们到forkchoice，最后resulting attestations被保存到fork choice pool
func (s *Service) batchForkChoiceAtts(ctx context.Context) error {
	ctx, span := trace.StartSpan(ctx, "Operations.attestations.batchForkChoiceAtts")
	defer span.End()

	// 先对unaggregated attestations做一次聚合
	if err := s.cfg.Pool.AggregateUnaggregatedAttestations(ctx); err != nil {
		return err
	}
	atts := append(s.cfg.Pool.AggregatedAttestations(), s.cfg.Pool.BlockAttestations()...)
	atts = append(atts, s.cfg.Pool.ForkchoiceAttestations()...)

	attsByDataRoot := make(map[[32]byte][]*ethpb.Attestation, len(atts))

	// Consolidate attestations by aggregating them by similar data root.
	// 将有着共同的data root的attestations聚合
	for _, att := range atts {
		seen, err := s.seen(att)
		if err != nil {
			return err
		}
		if seen {
			// 已经看到过的，直接跳过
			continue
		}

		attDataRoot, err := att.Data.HashTreeRoot()
		if err != nil {
			return err
		}
		attsByDataRoot[attDataRoot] = append(attsByDataRoot[attDataRoot], att)
	}

	for _, atts := range attsByDataRoot {
		// 聚合并且保存fork choice atts
		if err := s.aggregateAndSaveForkChoiceAtts(atts); err != nil {
			return err
		}
	}

	for _, a := range s.cfg.Pool.BlockAttestations() {
		// 删除block attestations
		if err := s.cfg.Pool.DeleteBlockAttestation(a); err != nil {
			return err
		}
	}

	return nil
}

// This aggregates a list of attestations using the aggregation algorithm defined in AggregateAttestations
// and saves the attestations for fork choice.
// 这个函数聚合一系列的attestations，使用定义在AggregateAttestations中的聚合算法，
// 并且保存attestations，为了fork choice
func (s *Service) aggregateAndSaveForkChoiceAtts(atts []*ethpb.Attestation) error {
	clonedAtts := make([]*ethpb.Attestation, len(atts))
	for i, a := range atts {
		clonedAtts[i] = ethpb.CopyAttestation(a)
	}
	aggregatedAtts, err := attaggregation.Aggregate(clonedAtts)
	if err != nil {
		return err
	}

	return s.cfg.Pool.SaveForkchoiceAttestations(aggregatedAtts)
}

// This checks if the attestation has previously been aggregated for fork choice
// return true if yes, false if no.
// 检查是否attestation之前已经聚合了，对于fork choice store，返回true，如果是的话，否则返回false
func (s *Service) seen(att *ethpb.Attestation) (bool, error) {
	attRoot, err := hash.HashProto(att.Data)
	if err != nil {
		return false, err
	}
	incomingBits := att.AggregationBits
	// 获取已经处理过的aggregation roots
	savedBits, ok := s.forkChoiceProcessedRoots.Get(attRoot)
	if ok {
		savedBitlist, ok := savedBits.(bitfield.Bitlist)
		if !ok {
			return false, errors.New("not a bit field")
		}
		if savedBitlist.Len() == incomingBits.Len() {
			// Returns true if the node has seen all the bits in the new bit field of the incoming attestation.
			// 返回true，如果node已经看到了所有incoming attestation的新的bit field
			if bytes.Equal(savedBitlist, incomingBits) {
				return true, nil
			}
			if c, err := savedBitlist.Contains(incomingBits); err != nil {
				return false, err
			} else if c {
				return true, nil
			}
			var err error
			// Update the bit fields by Or'ing them with the new ones.
			// 更新bit fields，通过将新的进行或操作
			incomingBits, err = incomingBits.Or(savedBitlist)
			if err != nil {
				return false, err
			}
		}
	}

	// 添加到forkchoice processed roots
	s.forkChoiceProcessedRoots.Add(attRoot, incomingBits)
	return false, nil
}
