// Package depositcache is the source of validator deposits maintained
// in-memory by the beacon node – deposits processed from the
// eth1 powchain are then stored in this cache to be accessed by
// any other service during a beacon node's runtime.
// depositcache包是validator deposits的源泉，由beacon node维护在内存中
// 被eth1 powchain处理的deposits之后被存储在这个缓存中，让beacon node的运行时
// 其他服务访问
package depositcache

import (
	"context"
	"encoding/hex"
	"math/big"
	"sort"
	"sync"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	fieldparams "github.com/prysmaticlabs/prysm/v3/config/fieldparams"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/container/trie"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

var (
	historicalDepositsCount = promauto.NewCounter(prometheus.CounterOpts{
		Name: "beacondb_all_deposits",
		Help: "The number of total deposits in the beaconDB in-memory database",
	})
)

// DepositFetcher defines a struct which can retrieve deposit information from a store.
// DepositFetcher定义了一个结构，可以从一个store中获取deposit信息
type DepositFetcher interface {
	AllDeposits(ctx context.Context, untilBlk *big.Int) []*ethpb.Deposit
	DepositByPubkey(ctx context.Context, pubKey []byte) (*ethpb.Deposit, *big.Int)
	DepositsNumberAndRootAtHeight(ctx context.Context, blockHeight *big.Int) (uint64, [32]byte)
	FinalizedDeposits(ctx context.Context) *FinalizedDeposits
	NonFinalizedDeposits(ctx context.Context, lastFinalizedIndex int64, untilBlk *big.Int) []*ethpb.Deposit
}

// FinalizedDeposits stores the trie of deposits that have been included
// in the beacon state up to the latest finalized checkpoint.
// FinalizedDeposits存储已经包含在beacon state中的trie of deposits，直到最后的finalized checkpoint
type FinalizedDeposits struct {
	Deposits        *trie.SparseMerkleTrie
	MerkleTrieIndex int64
}

// DepositCache stores all in-memory deposit objects. This
// stores all the deposit related data that is required by the beacon-node.
// DepositCache存储所有内存中的deposit对象，它存储所有deposit相关的数据，beacon-node需要
type DepositCache struct {
	// Beacon chain deposits in memory.
	// 内存中的Beacon chain deposits
	pendingDeposits   []*ethpb.DepositContainer
	deposits          []*ethpb.DepositContainer
	finalizedDeposits *FinalizedDeposits
	depositsByKey     map[[fieldparams.BLSPubkeyLength]byte][]*ethpb.DepositContainer
	depositsLock      sync.RWMutex
}

// New instantiates a new deposit cache
// New实例化一个新的deposit cache
func New() (*DepositCache, error) {
	finalizedDepositsTrie, err := trie.NewTrie(params.BeaconConfig().DepositContractTreeDepth)
	if err != nil {
		return nil, err
	}

	// finalizedDeposits.MerkleTrieIndex is initialized to -1 because it represents the index of the last trie item.
	// Inserting the first item into the trie will set the value of the index to 0.
	// finalizedDeposits.MerkleTrieIndex初始化为-1，因为它代表最后一个trie item的索引
	// 插入第一个item到trie，会设置index的值为0
	return &DepositCache{
		pendingDeposits:   []*ethpb.DepositContainer{},
		deposits:          []*ethpb.DepositContainer{},
		depositsByKey:     map[[fieldparams.BLSPubkeyLength]byte][]*ethpb.DepositContainer{},
		finalizedDeposits: &FinalizedDeposits{Deposits: finalizedDepositsTrie, MerkleTrieIndex: -1},
	}, nil
}

// InsertDeposit into the database. If deposit or block number are nil
// then this method does nothing.
// InsertDeposit插入deposit到db中，如果deposit或者block number为nil，则这个方法什么都不做
func (dc *DepositCache) InsertDeposit(ctx context.Context, d *ethpb.Deposit, blockNum uint64, index int64, depositRoot [32]byte) error {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.InsertDeposit")
	defer span.End()
	if d == nil {
		log.WithFields(logrus.Fields{
			"block":        blockNum,
			"deposit":      d,
			"index":        index,
			"deposit root": hex.EncodeToString(depositRoot[:]),
		}).Warn("Ignoring nil deposit insertion")
		return errors.New("nil deposit inserted into the cache")
	}
	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()

	if int(index) != len(dc.deposits) {
		return errors.Errorf("wanted deposit with index %d to be inserted but received %d", len(dc.deposits), index)
	}
	// Keep the slice sorted on insertion in order to avoid costly sorting on retrieval.
	// 保持slice有序，在插入的时候，为了避免在获取的时候排序的损耗
	heightIdx := sort.Search(len(dc.deposits), func(i int) bool { return dc.deposits[i].Index >= index })
	depCtr := &ethpb.DepositContainer{Deposit: d, Eth1BlockHeight: blockNum, DepositRoot: depositRoot[:], Index: index}
	newDeposits := append(
		[]*ethpb.DepositContainer{depCtr},
		dc.deposits[heightIdx:]...)
	dc.deposits = append(dc.deposits[:heightIdx], newDeposits...)
	// Append the deposit to our map, in the event no deposits
	// exist for the pubkey , it is simply added to the map.
	// 扩展deposit到我们的map，万一对于pubkey没有deposits存在，这就只是简单地加到map中
	pubkey := bytesutil.ToBytes48(d.Data.PublicKey)
	dc.depositsByKey[pubkey] = append(dc.depositsByKey[pubkey], depCtr)
	historicalDepositsCount.Inc()
	return nil
}

// InsertDepositContainers inserts a set of deposit containers into our deposit cache.
// InsertDepositContainers插入一系列的deposit containers到deposit cache
func (dc *DepositCache) InsertDepositContainers(ctx context.Context, ctrs []*ethpb.DepositContainer) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.InsertDepositContainers")
	defer span.End()
	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()

	sort.SliceStable(ctrs, func(i int, j int) bool { return ctrs[i].Index < ctrs[j].Index })
	dc.deposits = ctrs
	for _, c := range ctrs {
		// Use a new value, as the reference
		// of c changes in the next iteration.
		newPtr := c
		pKey := bytesutil.ToBytes48(newPtr.Deposit.Data.PublicKey)
		dc.depositsByKey[pKey] = append(dc.depositsByKey[pKey], newPtr)
	}
	historicalDepositsCount.Add(float64(len(ctrs)))
}

// InsertFinalizedDeposits inserts deposits up to eth1DepositIndex (inclusive) into the finalized deposits cache.
// InsertFinalizedDeposits插入deposits直到eth1DepositIndex（包含）到finalized deposits cache
func (dc *DepositCache) InsertFinalizedDeposits(ctx context.Context, eth1DepositIndex int64) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.InsertFinalizedDeposits")
	defer span.End()
	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()

	depositTrie := dc.finalizedDeposits.Deposits
	insertIndex := int(dc.finalizedDeposits.MerkleTrieIndex + 1)

	// Don't insert into finalized trie if there is no deposit to
	// insert.
	if len(dc.deposits) == 0 {
		return
	}
	// In the event we have less deposits than we need to
	// finalize we finalize till the index on which we do have it.
	if len(dc.deposits) <= int(eth1DepositIndex) {
		eth1DepositIndex = int64(len(dc.deposits)) - 1
	}
	// If we finalize to some lower deposit index, we
	// ignore it.
	if int(eth1DepositIndex) < insertIndex {
		return
	}
	for _, d := range dc.deposits {
		if d.Index <= dc.finalizedDeposits.MerkleTrieIndex {
			continue
		}
		if d.Index > eth1DepositIndex {
			break
		}
		depHash, err := d.Deposit.Data.HashTreeRoot()
		if err != nil {
			log.WithError(err).Error("Could not hash deposit data. Finalized deposit cache not updated.")
			return
		}
		if err = depositTrie.Insert(depHash[:], insertIndex); err != nil {
			log.WithError(err).Error("Could not insert deposit hash")
			return
		}
		insertIndex++
	}

	dc.finalizedDeposits = &FinalizedDeposits{
		Deposits:        depositTrie,
		MerkleTrieIndex: eth1DepositIndex,
	}
}

// AllDepositContainers returns all historical deposit containers.
// AllDepositContainers返回所有历史上的deposit containers
func (dc *DepositCache) AllDepositContainers(ctx context.Context) []*ethpb.DepositContainer {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.AllDepositContainers")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()

	// Make a shallow copy of the deposits and return that. This way, the
	// caller can safely iterate over the returned list of deposits without
	// the possibility of new deposits showing up. If we were to return the
	// list without a copy, when a new deposit is added to the cache, it
	// would also be present in the returned value. This could result in a
	// race condition if the list is being iterated over.
	//
	// It's not necessary to make a deep copy of this list because the
	// deposits in the cache should never be modified. It is still possible
	// for the caller to modify one of the underlying deposits and modify
	// the cache, but that's not a race condition. Also, a deep copy would
	// take too long and use too much memory.
	deposits := make([]*ethpb.DepositContainer, len(dc.deposits))
	copy(deposits, dc.deposits)
	return deposits
}

// AllDeposits returns a list of historical deposits until the given block number
// (inclusive). If no block is specified then this method returns all historical deposits.
// AllDeposits返回一系列的historical deposits，直到给定的block number（包含）
// 如果没有指定block，则这个方法返回所有的historical deposits
func (dc *DepositCache) AllDeposits(ctx context.Context, untilBlk *big.Int) []*ethpb.Deposit {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.AllDeposits")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()

	return dc.allDeposits(untilBlk)
}

func (dc *DepositCache) allDeposits(untilBlk *big.Int) []*ethpb.Deposit {
	var deposits []*ethpb.Deposit
	for _, ctnr := range dc.deposits {
		if untilBlk == nil || untilBlk.Uint64() >= ctnr.Eth1BlockHeight {
			deposits = append(deposits, ctnr.Deposit)
		}
	}
	return deposits
}

// DepositsNumberAndRootAtHeight returns number of deposits made up to blockheight and the
// root that corresponds to the latest deposit at that blockheight.
// DepositsNumberAndRootAtHeight返回到达blockHeight的deposits的数目以及root，对应最新的deposit
// 在blockheight
func (dc *DepositCache) DepositsNumberAndRootAtHeight(ctx context.Context, blockHeight *big.Int) (uint64, [32]byte) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.DepositsNumberAndRootAtHeight")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()
	heightIdx := sort.Search(len(dc.deposits), func(i int) bool { return dc.deposits[i].Eth1BlockHeight > blockHeight.Uint64() })
	// send the deposit root of the empty trie, if eth1follow distance is greater than the time of the earliest
	// deposit.
	if heightIdx == 0 {
		return 0, [32]byte{}
	}
	return uint64(heightIdx), bytesutil.ToBytes32(dc.deposits[heightIdx-1].DepositRoot)
}

// DepositByPubkey looks through historical deposits and finds one which contains
// a certain public key within its deposit data.
// DepositByPubkey查找historical deposits并且找到在deposit data中包含特定public key的deposit
func (dc *DepositCache) DepositByPubkey(ctx context.Context, pubKey []byte) (*ethpb.Deposit, *big.Int) {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.DepositByPubkey")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()

	var deposit *ethpb.Deposit
	var blockNum *big.Int
	deps, ok := dc.depositsByKey[bytesutil.ToBytes48(pubKey)]
	if !ok || len(deps) == 0 {
		return deposit, blockNum
	}
	// We always return the first deposit if a particular
	// validator key has multiple deposits assigned to
	// it.
	deposit = deps[0].Deposit
	blockNum = big.NewInt(int64(deps[0].Eth1BlockHeight))
	return deposit, blockNum
}

// FinalizedDeposits returns the finalized deposits trie.
// FinalizedDeposits返回finalized deposits trie
func (dc *DepositCache) FinalizedDeposits(ctx context.Context) *FinalizedDeposits {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.FinalizedDeposits")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()

	return &FinalizedDeposits{
		Deposits:        dc.finalizedDeposits.Deposits.Copy(),
		MerkleTrieIndex: dc.finalizedDeposits.MerkleTrieIndex,
	}
}

// NonFinalizedDeposits returns the list of non-finalized deposits until the given block number (inclusive).
// If no block is specified then this method returns all non-finalized deposits.
// NonFinalizedDeposits返回一系列non-finalized deposits，直到给定的block number（包含在内）
// 如果没有指定block，之后这个方法返回所有的non-finalized deposits
func (dc *DepositCache) NonFinalizedDeposits(ctx context.Context, lastFinalizedIndex int64, untilBlk *big.Int) []*ethpb.Deposit {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.NonFinalizedDeposits")
	defer span.End()
	dc.depositsLock.RLock()
	defer dc.depositsLock.RUnlock()

	if dc.finalizedDeposits == nil {
		return dc.allDeposits(untilBlk)
	}

	var deposits []*ethpb.Deposit
	for _, d := range dc.deposits {
		if (d.Index > lastFinalizedIndex) && (untilBlk == nil || untilBlk.Uint64() >= d.Eth1BlockHeight) {
			deposits = append(deposits, d.Deposit)
		}
	}

	return deposits
}

// PruneProofs removes proofs from all deposits whose index is equal or less than untilDepositIndex.
// PruneProofs移除所有deposits的proofs，如果它们的索引等于或者小于untilDepositIndex
func (dc *DepositCache) PruneProofs(ctx context.Context, untilDepositIndex int64) error {
	ctx, span := trace.StartSpan(ctx, "DepositsCache.PruneProofs")
	defer span.End()
	dc.depositsLock.Lock()
	defer dc.depositsLock.Unlock()

	if untilDepositIndex >= int64(len(dc.deposits)) {
		// 如果大于dc.deposits的数目
		untilDepositIndex = int64(len(dc.deposits) - 1)
	}

	for i := untilDepositIndex; i >= 0; i-- {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Finding a nil proof means that all proofs up to this deposit have been already pruned.
		// 找到一个nil proof，意味着到它为止的所有proofs已经被清除了
		if dc.deposits[i].Deposit.Proof == nil {
			break
		}
		dc.deposits[i].Deposit.Proof = nil
	}

	return nil
}
