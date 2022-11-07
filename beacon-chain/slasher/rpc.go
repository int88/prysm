package slasher

import (
	"context"

	"github.com/pkg/errors"
	slashertypes "github.com/prysmaticlabs/prysm/beacon-chain/slasher/types"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/time/slots"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HighestAttestations committed for an input list of validator indices.
// 从一系列的validator indices获取最高的attestations
func (s *Service) HighestAttestations(
	ctx context.Context, validatorIndices []types.ValidatorIndex,
) ([]*ethpb.HighestAttestation, error) {
	atts, err := s.serviceCfg.Database.HighestAttestations(ctx, validatorIndices)
	if err != nil {
		return nil, errors.Wrap(err, "could not get highest attestations from database")
	}
	return atts, nil
}

// IsSlashableBlock checks if an input block header is slashable
// with respect to historical block proposal data.
// IsSlashableBlock检查一个input block header是否是slashable，对于历史的block proposal data
func (s *Service) IsSlashableBlock(
	ctx context.Context, block *ethpb.SignedBeaconBlockHeader,
) (*ethpb.ProposerSlashing, error) {
	dataRoot, err := block.Header.HashTreeRoot()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not get block header hash tree root: %v", err)
	}
	signedBlockWrapper := &slashertypes.SignedBlockHeaderWrapper{
		SignedBeaconBlockHeader: block,
		SigningRoot:             dataRoot,
	}
	proposerSlashings, err := s.detectProposerSlashings(ctx, []*slashertypes.SignedBlockHeaderWrapper{signedBlockWrapper})
	if err != nil {
		// 检查proposal是否slashable失败
		return nil, status.Errorf(codes.Internal, "Could not check if proposal is slashable: %v", err)
	}
	if len(proposerSlashings) == 0 {
		return nil, nil
	}
	// 返回第一个proposerSlashings
	return proposerSlashings[0], nil
}

// IsSlashableAttestation checks if an input indexed attestation is slashable
// with respect to historical attestation data.
// IsSlashableAttestation检查一个输入的indexed attestation是否是slashable，对于historical
// attestation data
func (s *Service) IsSlashableAttestation(
	ctx context.Context, attestation *ethpb.IndexedAttestation,
) ([]*ethpb.AttesterSlashing, error) {
	dataRoot, err := attestation.Data.HashTreeRoot()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not get attestation data hash tree root: %v", err)
	}
	indexedAttWrapper := &slashertypes.IndexedAttestationWrapper{
		IndexedAttestation: attestation,
		SigningRoot:        dataRoot,
	}

	currentEpoch := slots.EpochsSinceGenesis(s.genesisTime)
	attesterSlashings, err := s.checkSlashableAttestations(ctx, currentEpoch, []*slashertypes.IndexedAttestationWrapper{indexedAttWrapper})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not check if attestation is slashable: %v", err)
	}
	if len(attesterSlashings) == 0 {
		// If the incoming attestations are not slashable, we mark them as saved in
		// slasher's DB storage to help us with future detection.
		// 如果incoming attestations是非slashable，我们标记他们，将他们保存在DB中，用于后续的检测
		if err := s.serviceCfg.Database.SaveAttestationRecordsForValidators(
			ctx, []*slashertypes.IndexedAttestationWrapper{indexedAttWrapper},
		); err != nil {
			return nil, status.Errorf(codes.Internal, "Could not save attestation records to DB: %v", err)
		}
		return nil, nil
	}
	return attesterSlashings, nil
}
