package client

import (
	"context"
	"fmt"
	"time"

	"github.com/prysmaticlabs/prysm/beacon-chain/core/signing"
	fieldparams "github.com/prysmaticlabs/prysm/config/fieldparams"
	"github.com/prysmaticlabs/prysm/config/params"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/crypto/bls"
	"github.com/prysmaticlabs/prysm/monitoring/tracing"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	validatorpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1/validator-client"
	prysmTime "github.com/prysmaticlabs/prysm/time"
	"github.com/prysmaticlabs/prysm/time/slots"
	"go.opencensus.io/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SubmitAggregateAndProof submits the validator's signed slot signature to the beacon node
// via gRPC. Beacon node will verify the slot signature and determine if the validator is also
// an aggregator. If yes, then beacon node will broadcast aggregated signature and
// proof on the validator's behalf.
// SubmitAggregateAndProof通过gRPC将validator的signed slot signature提交给beacon node
// Beacon node会校验slot signature并且决定是否validator是一个aggregator，如果是的话，beacon node
// 会广播aggregated signature以及proof，代表validator
func (v *validator) SubmitAggregateAndProof(ctx context.Context, slot types.Slot, pubKey [fieldparams.BLSPubkeyLength]byte) {
	ctx, span := trace.StartSpan(ctx, "validator.SubmitAggregateAndProof")
	defer span.End()

	span.AddAttributes(trace.StringAttribute("validator", fmt.Sprintf("%#x", pubKey)))
	fmtKey := fmt.Sprintf("%#x", pubKey[:])

	duty, err := v.duty(pubKey)
	if err != nil {
		// 不能获取validator assignment
		log.Errorf("Could not fetch validator assignment: %v", err)
		if v.emitAccountMetrics {
			ValidatorAggFailVec.WithLabelValues(fmtKey).Inc()
		}
		return
	}

	// Avoid sending beacon node duplicated aggregation requests.
	// 避免发送给beacon node重复的aggregation requests
	k := validatorSubscribeKey(slot, duty.CommitteeIndex)
	v.aggregatedSlotCommitteeIDCacheLock.Lock()
	if v.aggregatedSlotCommitteeIDCache.Contains(k) {
		v.aggregatedSlotCommitteeIDCacheLock.Unlock()
		return
	}
	v.aggregatedSlotCommitteeIDCache.Add(k, true)
	v.aggregatedSlotCommitteeIDCacheLock.Unlock()

	slotSig, err := v.signSlotWithSelectionProof(ctx, pubKey, slot)
	if err != nil {
		log.Errorf("Could not sign slot: %v", err)
		if v.emitAccountMetrics {
			ValidatorAggFailVec.WithLabelValues(fmtKey).Inc()
		}
		return
	}

	// As specified in spec, an aggregator should wait until two thirds of the way through slot
	// to broadcast the best aggregate to the global aggregate channel.
	// 如在spec中声明的，一个aggregator应该等待，直到三分之二，广播best aggregate到全局的aggregate channel
	// https://github.com/ethereum/consensus-specs/blob/v0.9.3/specs/validator/0_beacon-chain-validator.md#broadcast-aggregate
	v.waitToSlotTwoThirds(ctx, slot)

	res, err := v.validatorClient.SubmitAggregateSelectionProof(ctx, &ethpb.AggregateSelectionRequest{
		Slot:           slot,
		CommitteeIndex: duty.CommitteeIndex,
		PublicKey:      pubKey[:],
		SlotSignature:  slotSig,
	})
	if err != nil {
		s, ok := status.FromError(err)
		if ok && s.Code() == codes.NotFound {
			// 没有attestations用于聚合
			log.WithField("slot", slot).WithError(err).Warn("No attestations to aggregate")
		} else {
			log.WithField("slot", slot).WithError(err).Error("Could not submit slot signature to beacon node")
			if v.emitAccountMetrics {
				ValidatorAggFailVec.WithLabelValues(fmtKey).Inc()
			}
		}

		return
	}

	sig, err := v.aggregateAndProofSig(ctx, pubKey, res.AggregateAndProof, slot)
	if err != nil {
		log.Errorf("Could not sign aggregate and proof: %v", err)
		return
	}
	_, err = v.validatorClient.SubmitSignedAggregateSelectionProof(ctx, &ethpb.SignedAggregateSubmitRequest{
		SignedAggregateAndProof: &ethpb.SignedAggregateAttestationAndProof{
			Message:   res.AggregateAndProof,
			Signature: sig,
		},
	})
	if err != nil {
		log.Errorf("Could not submit signed aggregate and proof to beacon node: %v", err)
		if v.emitAccountMetrics {
			ValidatorAggFailVec.WithLabelValues(fmtKey).Inc()
		}
		return
	}

	// 将aggregator indices添加到logs
	if err := v.addIndicesToLog(duty); err != nil {
		log.Errorf("Could not add aggregator indices to logs: %v", err)
		if v.emitAccountMetrics {
			ValidatorAggFailVec.WithLabelValues(fmtKey).Inc()
		}
		return
	}
	if v.emitAccountMetrics {
		ValidatorAggSuccessVec.WithLabelValues(fmtKey).Inc()
	}

}

// Signs input slot with domain selection proof. This is used to create the signature for aggregator selection.
// 它用于为aggregator selection创建签名
func (v *validator) signSlotWithSelectionProof(ctx context.Context, pubKey [fieldparams.BLSPubkeyLength]byte, slot types.Slot) (signature []byte, err error) {
	domain, err := v.domainData(ctx, slots.ToEpoch(slot), params.BeaconConfig().DomainSelectionProof[:])
	if err != nil {
		return nil, err
	}

	var sig bls.Signature
	sszUint := types.SSZUint64(slot)
	root, err := signing.ComputeSigningRoot(&sszUint, domain.SignatureDomain)
	if err != nil {
		return nil, err
	}
	sig, err = v.keyManager.Sign(ctx, &validatorpb.SignRequest{
		PublicKey:       pubKey[:],
		SigningRoot:     root[:],
		SignatureDomain: domain.SignatureDomain,
		Object:          &validatorpb.SignRequest_Slot{Slot: slot},
		SigningSlot:     slot,
	})
	if err != nil {
		return nil, err
	}

	return sig.Marshal(), nil
}

// waitToSlotTwoThirds waits until two third through the current slot period
// such that any attestations from this slot have time to reach the beacon node
// before creating the aggregated attestation.
// waitToSlotTwoThirds等待直到当前slot的三分之二，任何这个slot的attestations有时间能
// 到达beacon node，在创建aggregated attestation之前
func (v *validator) waitToSlotTwoThirds(ctx context.Context, slot types.Slot) {
	ctx, span := trace.StartSpan(ctx, "validator.waitToSlotTwoThirds")
	defer span.End()

	oneThird := slots.DivideSlotBy(3 /* one third of slot duration */)
	twoThird := oneThird + oneThird
	delay := twoThird

	startTime := slots.StartTime(v.genesisTime, slot)
	finalTime := startTime.Add(delay)
	wait := prysmTime.Until(finalTime)
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		tracing.AnnotateError(span, ctx.Err())
		return
	case <-t.C:
		return
	}
}

// This returns the signature of validator signing over aggregate and
// proof object.
// 返回validator的signature，签名aggregate以及proof object
func (v *validator) aggregateAndProofSig(ctx context.Context, pubKey [fieldparams.BLSPubkeyLength]byte, agg *ethpb.AggregateAttestationAndProof, slot types.Slot) ([]byte, error) {
	d, err := v.domainData(ctx, slots.ToEpoch(agg.Aggregate.Data.Slot), params.BeaconConfig().DomainAggregateAndProof[:])
	if err != nil {
		return nil, err
	}
	var sig bls.Signature
	root, err := signing.ComputeSigningRoot(agg, d.SignatureDomain)
	if err != nil {
		return nil, err
	}
	sig, err = v.keyManager.Sign(ctx, &validatorpb.SignRequest{
		PublicKey:       pubKey[:],
		SigningRoot:     root[:],
		SignatureDomain: d.SignatureDomain,
		Object:          &validatorpb.SignRequest_AggregateAttestationAndProof{AggregateAttestationAndProof: agg},
		SigningSlot:     slot,
	})
	if err != nil {
		return nil, err
	}

	return sig.Marshal(), nil
}

func (v *validator) addIndicesToLog(duty *ethpb.DutiesResponse_Duty) error {
	v.attLogsLock.Lock()
	defer v.attLogsLock.Unlock()

	for _, log := range v.attLogs {
		if duty.CommitteeIndex == log.data.CommitteeIndex {
			log.aggregatorIndices = append(log.aggregatorIndices, duty.ValidatorIndex)
		}
	}

	return nil
}
