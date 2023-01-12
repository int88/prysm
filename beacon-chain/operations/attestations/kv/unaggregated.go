package kv

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"go.opencensus.io/trace"
)

// SaveUnaggregatedAttestation saves an unaggregated attestation in cache.
// SaveUnaggregatedAttestation保存一个unaggregated attestation到缓存中
func (c *AttCaches) SaveUnaggregatedAttestation(att *ethpb.Attestation) error {
	if att == nil {
		return nil
	}
	if helpers.IsAggregated(att) {
		return errors.New("attestation is aggregated")
	}

	seen, err := c.hasSeenBit(att)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	r, err := hashFn(att)
	if err != nil {
		return errors.Wrap(err, "could not tree hash attestation")
	}
	att = ethpb.CopyAttestation(att) // Copied.
	c.unAggregateAttLock.Lock()
	defer c.unAggregateAttLock.Unlock()
	c.unAggregatedAtt[r] = att

	return nil
}

// SaveUnaggregatedAttestations saves a list of unaggregated attestations in cache.
func (c *AttCaches) SaveUnaggregatedAttestations(atts []*ethpb.Attestation) error {
	for _, att := range atts {
		if err := c.SaveUnaggregatedAttestation(att); err != nil {
			return err
		}
	}

	return nil
}

// UnaggregatedAttestations returns all the unaggregated attestations in cache.
// UnaggregatedAttestations返回所有unaggregated attestations
func (c *AttCaches) UnaggregatedAttestations() ([]*ethpb.Attestation, error) {
	c.unAggregateAttLock.Lock()
	defer c.unAggregateAttLock.Unlock()
	unAggregatedAtts := c.unAggregatedAtt
	atts := make([]*ethpb.Attestation, 0, len(unAggregatedAtts))
	for _, att := range unAggregatedAtts {
		seen, err := c.hasSeenBit(att)
		if err != nil {
			return nil, err
		}
		if !seen {
			atts = append(atts, ethpb.CopyAttestation(att) /* Copied */)
		}
	}
	return atts, nil
}

// UnaggregatedAttestationsBySlotIndex returns the unaggregated attestations in cache,
// filtered by committee index and slot.
// UnaggregatedAttestationsBySlotIndex返回缓存中未被聚合的attestations，通过committee index以及slot
// 进行过滤
func (c *AttCaches) UnaggregatedAttestationsBySlotIndex(ctx context.Context, slot types.Slot, committeeIndex types.CommitteeIndex) []*ethpb.Attestation {
	ctx, span := trace.StartSpan(ctx, "operations.attestations.kv.UnaggregatedAttestationsBySlotIndex")
	defer span.End()

	atts := make([]*ethpb.Attestation, 0)

	c.unAggregateAttLock.RLock()
	defer c.unAggregateAttLock.RUnlock()

	unAggregatedAtts := c.unAggregatedAtt
	for _, a := range unAggregatedAtts {
		// 利用slot以及committee index进行过滤
		if slot == a.Data.Slot && committeeIndex == a.Data.CommitteeIndex {
			atts = append(atts, a)
		}
	}

	return atts
}

// DeleteUnaggregatedAttestation deletes the unaggregated attestations in cache.
// DeleteUnaggregatedAttestation删除缓存中的unaggregated attestations
func (c *AttCaches) DeleteUnaggregatedAttestation(att *ethpb.Attestation) error {
	if att == nil {
		return nil
	}
	if helpers.IsAggregated(att) {
		return errors.New("attestation is aggregated")
	}

	// 已经被处理过了，直接插入
	if err := c.insertSeenBit(att); err != nil {
		return err
	}

	r, err := hashFn(att)
	if err != nil {
		return errors.Wrap(err, "could not tree hash attestation")
	}

	c.unAggregateAttLock.Lock()
	defer c.unAggregateAttLock.Unlock()
	delete(c.unAggregatedAtt, r)

	return nil
}

// DeleteSeenUnaggregatedAttestations deletes the unaggregated attestations in cache
// that have been already processed once. Returns number of attestations deleted.
// DeleteSeenUnaggregatedAttestations删除缓存中已经被处理一遍的unaggregated attestations
// 返回被删除的attestations的数目
func (c *AttCaches) DeleteSeenUnaggregatedAttestations() (int, error) {
	c.unAggregateAttLock.Lock()
	defer c.unAggregateAttLock.Unlock()

	count := 0
	for _, att := range c.unAggregatedAtt {
		if att == nil || helpers.IsAggregated(att) {
			continue
		}
		if seen, err := c.hasSeenBit(att); err == nil && seen {
			r, err := hashFn(att)
			if err != nil {
				return count, errors.Wrap(err, "could not tree hash attestation")
			}
			delete(c.unAggregatedAtt, r)
			count++
		}
	}
	return count, nil
}

// UnaggregatedAttestationCount returns the number of unaggregated attestations key in the pool.
func (c *AttCaches) UnaggregatedAttestationCount() int {
	c.unAggregateAttLock.RLock()
	defer c.unAggregateAttLock.RUnlock()
	return len(c.unAggregatedAtt)
}
