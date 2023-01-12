package kv

import (
	"github.com/pkg/errors"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
)

// SaveBlockAttestation saves an block attestation in cache.
// SaveBlockAttestation保存一个block attestation到缓存中
func (c *AttCaches) SaveBlockAttestation(att *ethpb.Attestation) error {
	if att == nil {
		return nil
	}
	r, err := hashFn(att.Data)
	if err != nil {
		return errors.Wrap(err, "could not tree hash attestation")
	}

	c.blockAttLock.Lock()
	defer c.blockAttLock.Unlock()
	atts, ok := c.blockAtt[r]
	if !ok {
		atts = make([]*ethpb.Attestation, 0, 1)
	}

	// Ensure that this attestation is not already fully contained in an existing attestation.
	// 确保这个attestation没有完全包含在一个已经存在的attestation
	for _, a := range atts {
		if c, err := a.AggregationBits.Contains(att.AggregationBits); err != nil {
			return err
		} else if c {
			return nil
		}
	}

	c.blockAtt[r] = append(atts, ethpb.CopyAttestation(att))

	return nil
}

// SaveBlockAttestations saves a list of block attestations in cache.
// SaveBlockAttestations保存一系列的block attestations到缓存中
func (c *AttCaches) SaveBlockAttestations(atts []*ethpb.Attestation) error {
	for _, att := range atts {
		if err := c.SaveBlockAttestation(att); err != nil {
			return err
		}
	}

	return nil
}

// BlockAttestations returns the block attestations in cache.
// BlockAttestations返回cache中的block attestations
func (c *AttCaches) BlockAttestations() []*ethpb.Attestation {
	atts := make([]*ethpb.Attestation, 0)

	c.blockAttLock.RLock()
	defer c.blockAttLock.RUnlock()
	for _, att := range c.blockAtt {
		atts = append(atts, att...)
	}

	return atts
}

// DeleteBlockAttestation deletes a block attestation in cache.
// DeleteBlockAttestation删除缓存中的一个block attestation
func (c *AttCaches) DeleteBlockAttestation(att *ethpb.Attestation) error {
	if att == nil {
		return nil
	}
	r, err := hashFn(att.Data)
	if err != nil {
		return errors.Wrap(err, "could not tree hash attestation")
	}

	c.blockAttLock.Lock()
	defer c.blockAttLock.Unlock()
	delete(c.blockAtt, r)

	return nil
}
