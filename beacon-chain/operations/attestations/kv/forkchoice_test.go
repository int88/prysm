package kv

import (
	"sort"
	"testing"

	"github.com/prysmaticlabs/go-bitfield"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/testing/assert"
	"github.com/prysmaticlabs/prysm/testing/require"
	"github.com/prysmaticlabs/prysm/testing/util"
)

func TestKV_Forkchoice_CanSaveRetrieve(t *testing.T) {
	cache := NewAttCaches()

	au := util.AttestationUtil{}
	att1 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 1}, AggregationBits: bitfield.Bitlist{0b1101}})
	att2 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 2}, AggregationBits: bitfield.Bitlist{0b1101}})
	att3 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 3}, AggregationBits: bitfield.Bitlist{0b1101}})
	atts := []*ethpb.Attestation{att1, att2, att3}

	for _, att := range atts {
		require.NoError(t, cache.SaveForkchoiceAttestation(att))
	}

	returned := cache.ForkchoiceAttestations()

	sort.Slice(returned, func(i, j int) bool {
		return returned[i].Data.Slot < returned[j].Data.Slot
	})

	assert.DeepEqual(t, atts, returned)
}

func TestKV_Forkchoice_CanDelete(t *testing.T) {
	cache := NewAttCaches()

	au := util.AttestationUtil{}
	att1 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 1}, AggregationBits: bitfield.Bitlist{0b1101}})
	att2 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 2}, AggregationBits: bitfield.Bitlist{0b1101}})
	att3 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 3}, AggregationBits: bitfield.Bitlist{0b1101}})
	atts := []*ethpb.Attestation{att1, att2, att3}

	for _, att := range atts {
		require.NoError(t, cache.SaveForkchoiceAttestation(att))
	}

	require.NoError(t, cache.DeleteForkchoiceAttestation(att1))
	require.NoError(t, cache.DeleteForkchoiceAttestation(att3))

	returned := cache.ForkchoiceAttestations()
	wanted := []*ethpb.Attestation{att2}
	assert.DeepEqual(t, wanted, returned)
}

func TestKV_Forkchoice_CanCount(t *testing.T) {
	cache := NewAttCaches()

	au := util.AttestationUtil{}
	att1 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 1}, AggregationBits: bitfield.Bitlist{0b1101}})
	att2 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 2}, AggregationBits: bitfield.Bitlist{0b1101}})
	att3 := au.HydrateAttestation(&ethpb.Attestation{Data: &ethpb.AttestationData{Slot: 3}, AggregationBits: bitfield.Bitlist{0b1101}})
	atts := []*ethpb.Attestation{att1, att2, att3}

	for _, att := range atts {
		require.NoError(t, cache.SaveForkchoiceAttestation(att))
	}

	require.Equal(t, 3, cache.ForkchoiceAttestationCount())
}
