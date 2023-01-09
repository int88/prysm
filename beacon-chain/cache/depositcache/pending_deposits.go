package depositcache

import (
	"context"
	"math/big"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prysmaticlabs/prysm/v3/crypto/hash"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

var (
	pendingDepositsCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "beacondb_pending_deposits",
		Help: "The number of pending deposits in the beaconDB in-memory database",
	})
)

// PendingDepositsFetcher specifically outlines a struct that can retrieve deposits
// which have not yet been included in the chain.
// PendingDepositsFetcher特别勾勒出一个结构体，可以获取那些还没有包含进行chain的deposits
type PendingDepositsFetcher interface {
	PendingContainers(ctx context.Context, untilBlk *big.Int) []*ethpb.DepositContainer
}

// InsertPendingDeposit into the database. If deposit or block number are nil
// then this method does nothing.
// InsertPendingDeposit插入pending deposit到db，如果deposit或者block number为nil，则这个方法什么都不做
func (dc *DepositCache) InsertPendingDeposit(ctx context.Context, d *ethpb.Deposit, blockNum uint64, index int64, depositRoot [32]byte) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.InsertPendingDeposit")
	defer span.End()
	if d == nil {
		log.WithFields(logrus.Fields{
			"block":   blockNum,
			"deposit": d,
		}).Debug("Ignoring nil deposit insertion")
		return
	}
	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()
	// 扩展pending deposits
	dc.pendingDeposits = append(dc.pendingDeposits,
		&ethpb.DepositContainer{Deposit: d, Eth1BlockHeight: blockNum, Index: index, DepositRoot: depositRoot[:]})
	pendingDepositsCount.Inc()
	span.AddAttributes(trace.Int64Attribute("count", int64(len(dc.pendingDeposits))))
}

// PendingDeposits returns a list of deposits until the given block number
// (inclusive). If no block is specified then this method returns all pending
// deposits.
// PendingDeposits返回一系列的deposits，直到给定的block number（包含）
// 如果没有指定block，则这个方法返回所有的pending deposits
func (dc *DepositCache) PendingDeposits(ctx context.Context, untilBlk *big.Int) []*ethpb.Deposit {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.PendingDeposits")
	defer span.End()

	depositCntrs := dc.PendingContainers(ctx, untilBlk)

	deposits := make([]*ethpb.Deposit, 0, len(depositCntrs))
	for _, dep := range depositCntrs {
		deposits = append(deposits, dep.Deposit)
	}

	return deposits
}

// PendingContainers returns a list of deposit containers until the given block number
// (inclusive).
// PendingContainers返回一系列的deposit containers，直到给定的block number（包括）
func (dc *DepositCache) PendingContainers(ctx context.Context, untilBlk *big.Int) []*ethpb.DepositContainer {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.PendingDeposits")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()

	depositCntrs := make([]*ethpb.DepositContainer, 0, len(dc.pendingDeposits))
	for _, ctnr := range dc.pendingDeposits {
		if untilBlk == nil || untilBlk.Uint64() >= ctnr.Eth1BlockHeight {
			depositCntrs = append(depositCntrs, ctnr)
		}
	}
	// Sort the deposits by Merkle index.
	// 按照Merkle index对deposits进行排序
	sort.SliceStable(depositCntrs, func(i, j int) bool {
		return depositCntrs[i].Index < depositCntrs[j].Index
	})

	span.AddAttributes(trace.Int64Attribute("count", int64(len(depositCntrs))))

	return depositCntrs
}

// RemovePendingDeposit from the database. The deposit is indexed by the
// Index. This method does nothing if deposit ptr is nil.
func (dc *DepositCache) RemovePendingDeposit(ctx context.Context, d *ethpb.Deposit) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.RemovePendingDeposit")
	defer span.End()

	if d == nil {
		log.Debug("Ignoring nil deposit removal")
		return
	}

	depRoot, err := hash.HashProto(d)
	if err != nil {
		log.WithError(err).Error("Could not remove deposit")
		return
	}

	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()

	idx := -1
	for i, ctnr := range dc.pendingDeposits {
		h, err := hash.HashProto(ctnr.Deposit)
		if err != nil {
			log.WithError(err).Error("Could not hash deposit")
			continue
		}
		if h == depRoot {
			idx = i
			break
		}
	}

	if idx >= 0 {
		dc.pendingDeposits = append(dc.pendingDeposits[:idx], dc.pendingDeposits[idx+1:]...)
		pendingDepositsCount.Dec()
	}
}

// PrunePendingDeposits removes any deposit which is older than the given deposit merkle tree index.
// PrunePendingDeposits移除任何的deposit，它比给定的deposit merkle tree index更老
func (dc *DepositCache) PrunePendingDeposits(ctx context.Context, merkleTreeIndex int64) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.PrunePendingDeposits")
	defer span.End()

	if merkleTreeIndex == 0 {
		log.Debug("Ignoring 0 deposit removal")
		return
	}

	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()

	cleanDeposits := make([]*ethpb.DepositContainer, 0, len(dc.pendingDeposits))
	for _, dp := range dc.pendingDeposits {
		// 保留index大于merkleTreeIndex的deposit
		if dp.Index >= merkleTreeIndex {
			cleanDeposits = append(cleanDeposits, dp)
		}
	}

	dc.pendingDeposits = cleanDeposits
	pendingDepositsCount.Set(float64(len(dc.pendingDeposits)))
}
