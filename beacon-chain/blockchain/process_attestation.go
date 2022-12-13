package blockchain

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1/attestation"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"go.opencensus.io/trace"
)

// OnAttestation is called whenever an attestation is received, verifies the attestation is valid and saves
// it to the DB. As a stateless function, this does not hold nor delay attestation based on the spec descriptions.
// The delay is handled by the caller in `processAttestations`.
// OnAttestation被调用，当一个attestation被接收到的时候，校验attestation是合法的并且保存到DB中，作为一个无状态
// 函数，它不会维护任何delay attestation，基于spec的描述，delay在`processAttestations`中被处理
//
// Spec pseudocode definition:
//
//					def on_attestation(store: Store, attestation: Attestation) -> None:
//					 """
//					 Run ``on_attestation`` upon receiving a new ``attestation`` from either within a block or directly on the wire.
//				  ``on_attestation``在收到一个新的``attestation``的时候被调用，从一个block或者直接从线上
//
//					 An ``attestation`` that is asserted as invalid may be valid at a later time,
//					 consider scheduling it for later processing in such case.
//			      一个被标记为非法的``attestation``可能在后面被标记为合法，考虑调度它在后续进行处理
//					 """
//					 validate_on_attestation(store, attestation)
//					 store_target_checkpoint_state(store, attestation.data.target)
//
//					 # Get state at the `target` to fully validate attestation
//		          获取在`target`的state来完整校验attestation
//					 target_state = store.checkpoint_states[attestation.data.target]
//					 indexed_attestation = get_indexed_attestation(target_state, attestation)
//					 assert is_valid_indexed_attestation(target_state, indexed_attestation)
//
//					 # Update latest messages for attesting indices
//	              # 更新最新的messages，对于attesting indices
//					 update_latest_messages(store, indexed_attestation.attesting_indices, attestation)
func (s *Service) OnAttestation(ctx context.Context, a *ethpb.Attestation) error {
	ctx, span := trace.StartSpan(ctx, "blockChain.onAttestation")
	defer span.End()

	if err := helpers.ValidateNilAttestation(a); err != nil {
		return err
	}
	if err := helpers.ValidateSlotTargetEpoch(a.Data); err != nil {
		return err
	}
	// 获取target checkpoint
	tgt := ethpb.CopyCheckpoint(a.Data.Target)

	// Note that target root check is ignored here because it was performed in sync's validation pipeline:
	// validate_aggregate_proof.go and validate_beacon_attestation.go
	// If missing target root were to fail in this method, it would have just failed in `getAttPreState`.

	// Retrieve attestation's data beacon block pre state. Advance pre state to latest epoch if necessary and
	// save it to the cache.
	// 获取attestation的data里的beacon block pre state，移动prestate到最新的epoch，如果需要的话，保存它到cache中
	baseState, err := s.getAttPreState(ctx, tgt)
	if err != nil {
		return err
	}

	genesisTime := uint64(s.genesisTime.Unix())

	// Verify attestation target is from current epoch or previous epoch.
	// 校验attestation target来自当前的epoch或者之前的epoch
	if err := verifyAttTargetEpoch(ctx, genesisTime, uint64(time.Now().Unix()), tgt); err != nil {
		return err
	}

	// Verify attestation beacon block is known and not from the future.
	// 校验attestation beacon block是已知的并且不是来自未来
	if err := s.verifyBeaconBlock(ctx, a.Data); err != nil {
		return errors.Wrap(err, "could not verify attestation beacon block")
	}

	// Note that LMG GHOST and FFG consistency check is ignored because it was performed in sync's validation pipeline:
	// validate_aggregate_proof.go and validate_beacon_attestation.go

	// Verify attestations can only affect the fork choice of subsequent slots.
	// 校验attestations只会影响后续slots的fork choice
	if err := slots.VerifyTime(genesisTime, a.Data.Slot+1, params.BeaconNetworkConfig().MaximumGossipClockDisparity); err != nil {
		return err
	}

	// Use the target state to verify attesting indices are valid.
	// 使用target state来校验attesting indices是合法的
	committee, err := helpers.BeaconCommitteeFromState(ctx, baseState, a.Data.Slot, a.Data.CommitteeIndex)
	if err != nil {
		return err
	}
	indexedAtt, err := attestation.ConvertToIndexed(ctx, a, committee)
	if err != nil {
		return err
	}
	if err := attestation.IsValidAttestationIndices(ctx, indexedAtt); err != nil {
		return err
	}

	// Note that signature verification is ignored here because it was performed in sync's validation pipeline:
	// validate_aggregate_proof.go and validate_beacon_attestation.go
	// We assume trusted attestation in this function has verified signature.

	// Update forkchoice store with the new attestation for updating weight.
	// 用新的attestation更新forkchoice store，用于更新weight
	s.cfg.ForkChoiceStore.ProcessAttestation(ctx, indexedAtt.AttestingIndices, bytesutil.ToBytes32(a.Data.BeaconBlockRoot), a.Data.Target.Epoch)

	return nil
}
