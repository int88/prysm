package attestations

import (
	"time"

	"github.com/prysmaticlabs/prysm/v3/config/params"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	prysmTime "github.com/prysmaticlabs/prysm/v3/time"
)

// pruneAttsPool prunes attestations pool on every slot interval.
// pruneAttsPool移除attestations pool，在每个slot interval
func (s *Service) pruneAttsPool() {
	ticker := time.NewTicker(s.cfg.pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.pruneExpiredAtts()
			s.updateMetrics()
		case <-s.ctx.Done():
			log.Debug("Context closed, exiting routine")
			return
		}
	}
}

// This prunes expired attestations from the pool.
// 从pool中移除过期的attestations
func (s *Service) pruneExpiredAtts() {
	aggregatedAtts := s.cfg.Pool.AggregatedAttestations()
	for _, att := range aggregatedAtts {
		if s.expired(att.Data.Slot) {
			// 删除聚合的attestation
			if err := s.cfg.Pool.DeleteAggregatedAttestation(att); err != nil {
				// 删除过期的aggregated attestation失败
				log.WithError(err).Error("Could not delete expired aggregated attestation")
			}
			expiredAggregatedAtts.Inc()
		}
	}

	// 删除seen unaggregated attestations
	if _, err := s.cfg.Pool.DeleteSeenUnaggregatedAttestations(); err != nil {
		log.WithError(err).Error("Cannot delete seen attestations")
	}
	unAggregatedAtts, err := s.cfg.Pool.UnaggregatedAttestations()
	if err != nil {
		log.WithError(err).Error("Could not get unaggregated attestations")
		return
	}
	for _, att := range unAggregatedAtts {
		if s.expired(att.Data.Slot) {
			if err := s.cfg.Pool.DeleteUnaggregatedAttestation(att); err != nil {
				// 不能删除已经过期的unaggregated attestation
				log.WithError(err).Error("Could not delete expired unaggregated attestation")
			}
			expiredUnaggregatedAtts.Inc()
		}
	}

	blockAtts := s.cfg.Pool.BlockAttestations()
	for _, att := range blockAtts {
		if s.expired(att.Data.Slot) {
			// 删除block attestations
			if err := s.cfg.Pool.DeleteBlockAttestation(att); err != nil {
				log.WithError(err).Error("Could not delete expired block attestation")
			}
		}
		expiredBlockAtts.Inc()
	}
}

// Return true if the input slot has been expired.
// 返回true，如果输入的slot已经过时了
// Expired is defined as one epoch behind than current time.
// 过时被定义为当前时间的一个epoch之前
func (s *Service) expired(slot types.Slot) bool {
	expirationSlot := slot + params.BeaconConfig().SlotsPerEpoch
	// 转换为过期时间
	expirationTime := s.genesisTime + uint64(expirationSlot.Mul(params.BeaconConfig().SecondsPerSlot))
	currentTime := uint64(prysmTime.Now().Unix())
	return currentTime >= expirationTime
}
