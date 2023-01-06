package validator

import (
	"context"
	"fmt"

	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition/interop"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/crypto/bls"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	synccontribution "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1/attestation/aggregation/sync_contribution"
	"go.opencensus.io/trace"
)

func (vs *Server) BuildAltairBeaconBlock(ctx context.Context, req *ethpb.BlockRequest) (*ethpb.BeaconBlockAltair, error) {
	ctx, span := trace.StartSpan(ctx, "ProposerServer.BuildAltairBeaconBlock")
	defer span.End()
	// 首先构建phase0 block
	blkData, err := vs.buildPhase0BlockData(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("could not build block data: %v", err)
	}

	// Use zero hash as stub for state root to compute later.
	stateRoot := params.BeaconConfig().ZeroHash[:]

	// No need for safe sub as req.Slot cannot be 0 if requesting Altair blocks. If 0, we will be throwing
	// an error in the first validity check of this endpoint.
	// 不需要safe sub，因为req.Slot不可能为0，如果请求的是Altair blocks，如果为0，我们会抛出一个error
	// 在这个endpoint的开始的validity check
	syncAggregate, err := vs.getSyncAggregate(ctx, req.Slot-1, bytesutil.ToBytes32(blkData.ParentRoot))
	if err != nil {
		return nil, err
	}

	return &ethpb.BeaconBlockAltair{
		Slot:          req.Slot,
		ParentRoot:    blkData.ParentRoot,
		StateRoot:     stateRoot,
		ProposerIndex: blkData.ProposerIdx,
		Body: &ethpb.BeaconBlockBodyAltair{
			Eth1Data:          blkData.Eth1Data,
			Deposits:          blkData.Deposits,
			Attestations:      blkData.Attestations,
			RandaoReveal:      req.RandaoReveal,
			ProposerSlashings: blkData.ProposerSlashings,
			AttesterSlashings: blkData.AttesterSlashings,
			VoluntaryExits:    blkData.VoluntaryExits,
			Graffiti:          blkData.Graffiti[:],
			SyncAggregate:     syncAggregate,
		},
	}, nil
}

func (vs *Server) getAltairBeaconBlock(ctx context.Context, req *ethpb.BlockRequest) (*ethpb.BeaconBlockAltair, error) {
	ctx, span := trace.StartSpan(ctx, "ProposerServer.getAltairBeaconBlock")
	defer span.End()
	blk, err := vs.BuildAltairBeaconBlock(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("could not build block data: %v", err)
	}
	// Compute state root with the newly constructed block.
	wsb, err := blocks.NewSignedBeaconBlock(
		&ethpb.SignedBeaconBlockAltair{Block: blk, Signature: make([]byte, 96)},
	)
	if err != nil {
		return nil, err
	}
	stateRoot, err := vs.computeStateRoot(ctx, wsb)
	if err != nil {
		interop.WriteBlockToDisk(wsb, true /*failed*/)
		return nil, fmt.Errorf("could not compute state root: %v", err)
	}
	blk.StateRoot = stateRoot
	return blk, nil
}

// getSyncAggregate retrieves the sync contributions from the pool to construct the sync aggregate object.
// The contributions are filtered based on matching of the input root and slot then profitability.
// getSyncAggregate从pool中获取sync contributions，来构建sync aggregate对象，基于input root的匹配以及slot
// 来过滤contributions，之后是
func (vs *Server) getSyncAggregate(ctx context.Context, slot types.Slot, root [32]byte) (*ethpb.SyncAggregate, error) {
	ctx, span := trace.StartSpan(ctx, "ProposerServer.getSyncAggregate")
	defer span.End()

	// Contributions have to match the input root
	// Contributions需要匹配input root
	contributions, err := vs.SyncCommitteePool.SyncCommitteeContributions(slot)
	if err != nil {
		return nil, err
	}
	proposerContributions := proposerSyncContributions(contributions).filterByBlockRoot(root)

	// Each sync subcommittee is 128 bits and the sync committee is 512 bits for mainnet.
	var bitsHolder [][]byte
	for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
		bitsHolder = append(bitsHolder, ethpb.NewSyncCommitteeAggregationBits())
	}
	sigsHolder := make([]bls.Signature, 0, params.BeaconConfig().SyncCommitteeSize/params.BeaconConfig().SyncCommitteeSubnetCount)

	for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
		cs := proposerContributions.filterBySubIndex(i)
		aggregates, err := synccontribution.Aggregate(cs)
		if err != nil {
			return nil, err
		}

		// Retrieve the most profitable contribution
		// 获取最profitable的contribution
		deduped, err := proposerSyncContributions(aggregates).dedup()
		if err != nil {
			return nil, err
		}
		c := deduped.mostProfitable()
		if c == nil {
			continue
		}
		bitsHolder[i] = c.AggregationBits
		sig, err := bls.SignatureFromBytes(c.Signature)
		if err != nil {
			return nil, err
		}
		sigsHolder = append(sigsHolder, sig)
	}

	// Aggregate all the contribution bits and signatures.
	// 聚合所有的contribution bits以及signatures
	var syncBits []byte
	for _, b := range bitsHolder {
		syncBits = append(syncBits, b...)
	}
	syncSig := bls.AggregateSignatures(sigsHolder)
	var syncSigBytes [96]byte
	if syncSig == nil {
		syncSigBytes = [96]byte{0xC0} // Infinity signature if itself is nil.
	} else {
		syncSigBytes = bytesutil.ToBytes96(syncSig.Marshal())
	}

	return &ethpb.SyncAggregate{
		SyncCommitteeBits:      syncBits,
		SyncCommitteeSignature: syncSigBytes[:],
	}, nil
}
