// Package execution defines a runtime service which is tasked with
// communicating with an eth1 endpoint, processing logs from a deposit
// contract, and the latest eth1 data headers for usage in the beacon node.
// execution包定义了一个runtime service，用于和一个eth1 endpoint进行交互，处理来自一个deposit
// contract的logs，以及最新的eth1 data headers用于beacon node
package execution

import (
	"context"
	"fmt"
	"math/big"
	"reflect"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	gethRPC "github.com/ethereum/go-ethereum/rpc"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/cache/depositcache"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed"
	statefeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/state"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/execution/types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	native "github.com/prysmaticlabs/prysm/v3/beacon-chain/state/state-native"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stategen"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/container/trie"
	contracts "github.com/prysmaticlabs/prysm/v3/contracts/deposit"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	"github.com/prysmaticlabs/prysm/v3/monitoring/clientstats"
	"github.com/prysmaticlabs/prysm/v3/network"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	prysmTime "github.com/prysmaticlabs/prysm/v3/time"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"github.com/sirupsen/logrus"
)

var (
	validDepositsCount = promauto.NewCounter(prometheus.CounterOpts{
		Name: "powchain_valid_deposits_received",
		Help: "The number of valid deposits received in the deposit contract",
	})
	blockNumberGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "powchain_block_number",
		Help: "The current block number in the proof-of-work chain",
	})
	missedDepositLogsCount = promauto.NewCounter(prometheus.CounterOpts{
		Name: "powchain_missed_deposit_logs",
		Help: "The number of times a missed deposit log is detected",
	})
)

var (
	// time to wait before trying to reconnect with the eth1 node.
	backOffPeriod = 15 * time.Second
	// amount of times before we log the status of the eth1 dial attempt.
	// 在我们记录eth1 dial attempt的状态之前，重试的次数
	logThreshold = 8
	// period to log chainstart related information
	logPeriod = 1 * time.Minute
)

// ChainStartFetcher retrieves information pertaining to the chain start event
// of the beacon chain for usage across various services.
// ChainStartFetcher获取信息，关于beacon chain的chain start event，在其他服务使用
type ChainStartFetcher interface {
	ChainStartEth1Data() *ethpb.Eth1Data
	PreGenesisState() state.BeaconState
	ClearPreGenesisData()
}

// ChainInfoFetcher retrieves information about eth1 metadata at the Ethereum consensus genesis time.
// ChainInfoFetcher获取eth1 metadata的信息，关于Ethereum consensus genesis time信息
type ChainInfoFetcher interface {
	GenesisExecutionChainInfo() (uint64, *big.Int)
	ExecutionClientConnected() bool
	ExecutionClientEndpoint() string
	ExecutionClientConnectionErr() error
}

// POWBlockFetcher defines a struct that can retrieve mainchain blocks.
// POWBlockFetcher定义了一个结构用于获取mainchain blocks
type POWBlockFetcher interface {
	BlockTimeByHeight(ctx context.Context, height *big.Int) (uint64, error)
	BlockByTimestamp(ctx context.Context, time uint64) (*types.HeaderInfo, error)
	BlockHashByHeight(ctx context.Context, height *big.Int) (common.Hash, error)
	BlockExists(ctx context.Context, hash common.Hash) (bool, *big.Int, error)
}

// Chain defines a standard interface for the powchain service in Prysm.
// Chain定义了一个标准接口，在Prysm中用于powchain service
type Chain interface {
	ChainStartFetcher
	ChainInfoFetcher
	POWBlockFetcher
}

// RPCClient defines the rpc methods required to interact with the eth1 node.
// RPCClient定义了rpc方法，用于和eth1 node进行交互
type RPCClient interface {
	Close()
	BatchCall(b []gethRPC.BatchElem) error
	CallContext(ctx context.Context, result interface{}, method string, args ...interface{}) error
}

type RPCClientEmpty struct {
}

func (RPCClientEmpty) Close() {}
func (RPCClientEmpty) BatchCall([]gethRPC.BatchElem) error {
	return errors.New("rpc client is not initialized")
}

func (RPCClientEmpty) CallContext(context.Context, interface{}, string, ...interface{}) error {
	return errors.New("rpc client is not initialized")
}

// config defines a config struct for dependencies into the service.
type config struct {
	depositContractAddr     common.Address
	beaconDB                db.HeadAccessDatabase
	depositCache            *depositcache.DepositCache
	stateNotifier           statefeed.Notifier
	stateGen                *stategen.State
	eth1HeaderReqLimit      uint64
	beaconNodeStatsUpdater  BeaconNodeStatsUpdater
	currHttpEndpoint        network.Endpoint
	headers                 []string
	finalizedStateAtStartup state.BeaconState
}

// Service fetches important information about the canonical
// eth1 chain via a web3 endpoint using an ethclient.
// Service获取关于canonical eth1 chain的重要信息，通过web3 ednpoint，使用一个ethclient
// The beacon chain requires synchronization with the eth1 chain's current
// block hash, block number, and access to logs within the
// Validator Registration Contract on the eth1 chain to kick off the beacon
// chain's validator registration process.
// beacon chain需要和eth1 chain进行同步，关于它当前的block hash, block number以及在Validator Registration Contract
// 的日志访问，在eth1 chain来启动beacon chain的validator注册过程
type Service struct {
	connectedETH1      bool
	isRunning          bool
	processingLock     sync.RWMutex
	latestEth1DataLock sync.RWMutex
	cfg                *config
	ctx                context.Context
	cancel             context.CancelFunc
	eth1HeadTicker     *time.Ticker
	httpLogger         bind.ContractFilterer
	rpcClient          RPCClient
	// 用于存放block hash和block weight的缓存
	headerCache             *headerCache // cache to store block hash/block height.
	latestEth1Data          *ethpb.LatestETH1Data
	depositContractCaller   *contracts.DepositContractCaller
	depositTrie             *trie.SparseMerkleTrie
	chainStartData          *ethpb.ChainStartData
	lastReceivedMerkleIndex int64 // Keeps track of the last received index to prevent log spam.
	runError                error
	preGenesisState         state.BeaconState
}

// NewService sets up a new instance with an ethclient when given a web3 endpoint as a string in the config.
// NewService建立一个新的实例，有一个ethclient，给定一个web3 endpoint作为配置里的一个string
func NewService(ctx context.Context, opts ...Option) (*Service, error) {
	ctx, cancel := context.WithCancel(ctx)
	_ = cancel // govet fix for lost cancel. Cancel is handled in service.Stop()
	// 构建depositTrie
	depositTrie, err := trie.NewTrie(params.BeaconConfig().DepositContractTreeDepth)
	if err != nil {
		cancel()
		return nil, errors.Wrap(err, "could not set up deposit trie")
	}
	// 构建一个空的genesis state
	genState, err := transition.EmptyGenesisState()
	if err != nil {
		return nil, errors.Wrap(err, "could not set up genesis state")
	}

	s := &Service{
		ctx:       ctx,
		cancel:    cancel,
		rpcClient: RPCClientEmpty{},
		cfg: &config{
			beaconNodeStatsUpdater: &NopBeaconNodeStatsUpdater{},
			eth1HeaderReqLimit:     defaultEth1HeaderReqLimit,
		},
		latestEth1Data: &ethpb.LatestETH1Data{
			BlockHeight:        0,
			BlockTime:          0,
			BlockHash:          []byte{},
			LastRequestedBlock: 0,
		},
		headerCache: newHeaderCache(),
		depositTrie: depositTrie,
		chainStartData: &ethpb.ChainStartData{
			Eth1Data:           &ethpb.Eth1Data{},
			ChainstartDeposits: make([]*ethpb.Deposit, 0),
		},
		lastReceivedMerkleIndex: -1,
		preGenesisState:         genState,
		eth1HeadTicker:          time.NewTicker(time.Duration(params.BeaconConfig().SecondsPerETH1Block) * time.Second),
	}

	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}

	if err := s.ensureValidPowchainData(ctx); err != nil {
		return nil, errors.Wrap(err, "unable to validate powchain data")
	}

	eth1Data, err := s.cfg.beaconDB.ExecutionChainData(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "unable to retrieve eth1 data")
	}

	// 初始化eth1 data
	if err := s.initializeEth1Data(ctx, eth1Data); err != nil {
		return nil, err
	}
	return s, nil
}

// Start the powchain service's main event loop.
// 启动powchain service的main loop event
func (s *Service) Start() {
	// 设置和execution endpoint的连接
	if err := s.setupExecutionClientConnections(s.ctx, s.cfg.currHttpEndpoint); err != nil {
		log.WithError(err).Error("Could not connect to execution endpoint")
	}
	// If the chain has not started already and we don't have access to eth1 nodes, we will not be
	// able to generate the genesis state.
	// 如果chain没有启动并且不能访问eth1 nodes，我们不能产生genesis state
	if !s.chainStartData.Chainstarted && s.cfg.currHttpEndpoint.Url == "" {
		// check for genesis state before shutting down the node,
		// if a genesis state exists, we can continue on.
		// 检查genesis state，在关闭node之前，如果有genesis state存在，我们可以继续
		genState, err := s.cfg.beaconDB.GenesisState(s.ctx)
		if err != nil {
			log.Fatal(err)
		}
		if genState == nil || genState.IsNil() {
			log.Fatal("cannot create genesis state: no eth1 http endpoint defined")
		}
	}

	s.isRunning = true

	// Poll the execution client connection and fallback if errors occur.
	// 轮询execution client连接并且回退如果有错误的话
	s.pollConnectionStatus(s.ctx)

	// Check transition configuration for the engine API client in the background.
	// 后台检查transition配置，对于engine API client
	go s.checkTransitionConfiguration(s.ctx, make(chan *feed.Event, 1))

	go s.run(s.ctx.Done())
}

// Stop the web3 service's main event loop and associated goroutines.
func (s *Service) Stop() error {
	if s.cancel != nil {
		defer s.cancel()
	}
	if s.rpcClient != nil {
		s.rpcClient.Close()
	}
	return nil
}

// ClearPreGenesisData clears out the stored chainstart deposits and beacon state.
func (s *Service) ClearPreGenesisData() {
	s.chainStartData.ChainstartDeposits = []*ethpb.Deposit{}
	s.preGenesisState = &native.BeaconState{}
}

// ChainStartEth1Data returns the eth1 data at chainstart.
func (s *Service) ChainStartEth1Data() *ethpb.Eth1Data {
	return s.chainStartData.Eth1Data
}

// PreGenesisState returns a state that contains
// pre-chainstart deposits.
func (s *Service) PreGenesisState() state.BeaconState {
	return s.preGenesisState
}

// Status is service health checks. Return nil or error.
func (s *Service) Status() error {
	// Service don't start
	if !s.isRunning {
		return nil
	}
	// get error from run function
	return s.runError
}

// ExecutionClientConnected checks whether are connected via RPC.
func (s *Service) ExecutionClientConnected() bool {
	return s.connectedETH1
}

// ExecutionClientEndpoint returns the URL of the current, connected execution client.
func (s *Service) ExecutionClientEndpoint() string {
	return s.cfg.currHttpEndpoint.Url
}

// ExecutionClientConnectionErr returns the error (if any) of the current connection.
func (s *Service) ExecutionClientConnectionErr() error {
	return s.runError
}

func (s *Service) updateBeaconNodeStats() {
	bs := clientstats.BeaconNodeStats{}
	if s.ExecutionClientConnected() {
		bs.SyncEth1Connected = true
	}
	s.cfg.beaconNodeStatsUpdater.Update(bs)
}

func (s *Service) updateConnectedETH1(state bool) {
	s.connectedETH1 = state
	s.updateBeaconNodeStats()
}

// refers to the latest eth1 block which follows the condition: eth1_timestamp +
// SECONDS_PER_ETH1_BLOCK * ETH1_FOLLOW_DISTANCE <= current_unix_time
// 引用最新的eth1 block，符合以下条件：eth1_timestamp + SECONDS_PER_ETH1_BLOCK * ETH1_FOLLOW_DISTANCE
// <= current_unix_time
func (s *Service) followedBlockHeight(ctx context.Context) (uint64, error) {
	followTime := params.BeaconConfig().Eth1FollowDistance * params.BeaconConfig().SecondsPerETH1Block
	latestBlockTime := uint64(0)
	if s.latestEth1Data.BlockTime > followTime {
		latestBlockTime = s.latestEth1Data.BlockTime - followTime
		// This should only come into play in testnets - when the chain hasn't advanced past the follow distance,
		// we don't want to consider any block before the genesis block.
		if s.latestEth1Data.BlockHeight < params.BeaconConfig().Eth1FollowDistance {
			latestBlockTime = s.latestEth1Data.BlockTime
		}
	}
	blk, err := s.BlockByTimestamp(ctx, latestBlockTime)
	if err != nil {
		return 0, errors.Wrapf(err, "BlockByTimestamp=%d", latestBlockTime)
	}
	return blk.Number.Uint64(), nil
}

func (s *Service) initDepositCaches(ctx context.Context, ctrs []*ethpb.DepositContainer) error {
	if len(ctrs) == 0 {
		return nil
	}
	s.cfg.depositCache.InsertDepositContainers(ctx, ctrs)
	if !s.chainStartData.Chainstarted {
		// Do not add to pending cache if no genesis state exists.
		validDepositsCount.Add(float64(s.preGenesisState.Eth1DepositIndex()))
		return nil
	}
	genesisState, err := s.cfg.beaconDB.GenesisState(ctx)
	if err != nil {
		return err
	}
	// Default to all post-genesis deposits in
	// the event we cannot find a finalized state.
	currIndex := genesisState.Eth1DepositIndex()
	chkPt, err := s.cfg.beaconDB.FinalizedCheckpoint(ctx)
	if err != nil {
		return err
	}
	rt := bytesutil.ToBytes32(chkPt.Root)
	if rt != [32]byte{} {
		fState := s.cfg.finalizedStateAtStartup
		if fState == nil || fState.IsNil() {
			return errors.Errorf("finalized state with root %#x is nil", rt)
		}
		// Set deposit index to the one in the current archived state.
		currIndex = fState.Eth1DepositIndex()

		// When a node pauses for some time and starts again, the deposits to finalize
		// accumulates. We finalize them here before we are ready to receive a block.
		// Otherwise, the first few blocks will be slower to compute as we will
		// hold the lock and be busy finalizing the deposits.
		// The deposit index in the state is always the index of the next deposit
		// to be included (rather than the last one to be processed). This was most likely
		// done as the state cannot represent signed integers.
		actualIndex := int64(currIndex) - 1 // lint:ignore uintcast -- deposit index will not exceed int64 in your lifetime.
		s.cfg.depositCache.InsertFinalizedDeposits(ctx, actualIndex)

		// Deposit proofs are only used during state transition and can be safely removed to save space.
		if err = s.cfg.depositCache.PruneProofs(ctx, actualIndex); err != nil {
			return errors.Wrap(err, "could not prune deposit proofs")
		}
	}
	validDepositsCount.Add(float64(currIndex))
	// Only add pending deposits if the container slice length
	// is more than the current index in state.
	if uint64(len(ctrs)) > currIndex {
		for _, c := range ctrs[currIndex:] {
			s.cfg.depositCache.InsertPendingDeposit(ctx, c.Deposit, c.Eth1BlockHeight, c.Index, bytesutil.ToBytes32(c.DepositRoot))
		}
	}
	return nil
}

// processBlockHeader adds a newly observed eth1 block to the block cache and
// updates the latest blockHeight, blockHash, and blockTime properties of the service.
func (s *Service) processBlockHeader(header *types.HeaderInfo) {
	defer safelyHandlePanic()
	blockNumberGauge.Set(float64(header.Number.Int64()))
	s.latestEth1DataLock.Lock()
	s.latestEth1Data.BlockHeight = header.Number.Uint64()
	s.latestEth1Data.BlockHash = header.Hash.Bytes()
	s.latestEth1Data.BlockTime = header.Time
	s.latestEth1DataLock.Unlock()
	log.WithFields(logrus.Fields{
		"blockNumber": s.latestEth1Data.BlockHeight,
		"blockHash":   hexutil.Encode(s.latestEth1Data.BlockHash),
	}).Debug("Latest eth1 chain event")
}

// batchRequestHeaders requests the block range specified in the arguments. Instead of requesting
// each block in one call, it batches all requests into a single rpc call.
func (s *Service) batchRequestHeaders(startBlock, endBlock uint64) ([]*types.HeaderInfo, error) {
	if startBlock > endBlock {
		return nil, fmt.Errorf("start block height %d cannot be > end block height %d", startBlock, endBlock)
	}
	requestRange := (endBlock - startBlock) + 1
	elems := make([]gethRPC.BatchElem, 0, requestRange)
	headers := make([]*types.HeaderInfo, 0, requestRange)
	if requestRange == 0 {
		return headers, nil
	}
	for i := startBlock; i <= endBlock; i++ {
		header := &types.HeaderInfo{}
		elems = append(elems, gethRPC.BatchElem{
			Method: "eth_getBlockByNumber",
			Args:   []interface{}{hexutil.EncodeBig(big.NewInt(0).SetUint64(i)), false},
			Result: header,
			Error:  error(nil),
		})
		headers = append(headers, header)

	}
	ioErr := s.rpcClient.BatchCall(elems)
	if ioErr != nil {
		return nil, ioErr
	}
	for _, e := range elems {
		if e.Error != nil {
			return nil, e.Error
		}
	}
	for _, h := range headers {
		if h != nil {
			if err := s.headerCache.AddHeader(h); err != nil {
				return nil, err
			}
		}
	}
	return headers, nil
}

// safelyHandleHeader will recover and log any panic that occurs from the block
func safelyHandlePanic() {
	if r := recover(); r != nil {
		log.WithFields(logrus.Fields{
			"r": r,
		}).Error("Panicked when handling data from ETH 1.0 Chain! Recovering...")

		debug.PrintStack()
	}
}

func (s *Service) handleETH1FollowDistance() {
	defer safelyHandlePanic()
	ctx := s.ctx

	// use a 5 minutes timeout for block time, because the max mining time is 278 sec (block 7208027)
	// (analyzed the time of the block from 2018-09-01 to 2019-02-13)
	fiveMinutesTimeout := prysmTime.Now().Add(-5 * time.Minute)
	// check that web3 client is syncing
	if time.Unix(int64(s.latestEth1Data.BlockTime), 0).Before(fiveMinutesTimeout) {
		log.Warn("Execution client is not syncing")
	}
	if !s.chainStartData.Chainstarted {
		if err := s.processChainStartFromBlockNum(ctx, big.NewInt(int64(s.latestEth1Data.LastRequestedBlock))); err != nil {
			s.runError = errors.Wrap(err, "processChainStartFromBlockNum")
			log.Error(err)
			return
		}
	}

	// If the last requested block has not changed,
	// we do not request batched logs as this means there are no new
	// logs for the powchain service to process. Also it is a potential
	// failure condition as would mean we have not respected the protocol threshold.
	if s.latestEth1Data.LastRequestedBlock == s.latestEth1Data.BlockHeight {
		log.Error("Beacon node is not respecting the follow distance")
		return
	}
	if err := s.requestBatchedHeadersAndLogs(ctx); err != nil {
		s.runError = errors.Wrap(err, "requestBatchedHeadersAndLogs")
		log.Error(err)
		return
	}
	// Reset the Status.
	if s.runError != nil {
		s.runError = nil
	}
}

func (s *Service) initPOWService() {
	// Use a custom logger to only log errors
	logCounter := 0
	errorLogger := func(err error, msg string) {
		if logCounter > logThreshold {
			log.WithError(err).Error(msg)
			logCounter = 0
		}
		logCounter++
	}

	// Run in a select loop to retry in the event of any failures.
	// 运行一个select loop，在有任何失败的时候retry
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			ctx := s.ctx
			header, err := s.HeaderByNumber(ctx, nil)
			if err != nil {
				err = errors.Wrap(err, "HeaderByNumber")
				s.retryExecutionClientConnection(ctx, err)
				errorLogger(err, "Unable to retrieve latest execution client header")
				continue
			}

			s.latestEth1DataLock.Lock()
			s.latestEth1Data.BlockHeight = header.Number.Uint64()
			s.latestEth1Data.BlockHash = header.Hash.Bytes()
			s.latestEth1Data.BlockTime = header.Time
			s.latestEth1DataLock.Unlock()

			if err := s.processPastLogs(ctx); err != nil {
				err = errors.Wrap(err, "processPastLogs")
				s.retryExecutionClientConnection(ctx, err)
				errorLogger(
					err,
					// 不能处理deposit contract logs，也许execution client没有完全同步
					"Unable to process past deposit contract logs, perhaps your execution client is not fully synced",
				)
				continue
			}
			// Cache eth1 headers from our voting period.
			// 缓存eth1 headers，来自我们的voting period
			if err := s.cacheHeadersForEth1DataVote(ctx); err != nil {
				err = errors.Wrap(err, "cacheHeadersForEth1DataVote")
				s.retryExecutionClientConnection(ctx, err)
				if errors.Is(err, errBlockTimeTooLate) {
					log.WithError(err).Debug("Unable to cache headers for execution client votes")
				} else {
					errorLogger(err, "Unable to cache headers for execution client votes")
				}
				continue
			}
			// Handle edge case with embedded genesis state by fetching genesis header to determine
			// its height.
			// 处理边缘情况，用内置的genesis state，通过抓取genesis header来决定它的height
			if s.chainStartData.Chainstarted && s.chainStartData.GenesisBlock == 0 {
				genHash := common.BytesToHash(s.chainStartData.Eth1Data.BlockHash)
				genBlock := s.chainStartData.GenesisBlock
				// In the event our provided chainstart data references a non-existent block hash,
				// we assume the genesis block to be 0.
				// 万一我们提供的chainstart data引用一个不存在的block hash，我们假设genesis block为0
				if genHash != [32]byte{} {
					genHeader, err := s.HeaderByHash(ctx, genHash)
					if err != nil {
						err = errors.Wrapf(err, "HeaderByHash, hash=%#x", genHash)
						s.retryExecutionClientConnection(ctx, err)
						errorLogger(err, "Unable to retrieve proof-of-stake genesis block data")
						continue
					}
					genBlock = genHeader.Number.Uint64()
				}
				s.chainStartData.GenesisBlock = genBlock
				if err := s.savePowchainData(ctx); err != nil {
					err = errors.Wrap(err, "savePowchainData")
					s.retryExecutionClientConnection(ctx, err)
					errorLogger(err, "Unable to save execution client data")
					continue
				}
			}
			return
		}
	}
}

// run subscribes to all the services for the eth1 chain.
// run订阅所有的services，对于eth1 chain
func (s *Service) run(done <-chan struct{}) {
	s.runError = nil

	s.initPOWService()

	chainstartTicker := time.NewTicker(logPeriod)
	defer chainstartTicker.Stop()

	for {
		select {
		case <-done:
			s.isRunning = false
			s.runError = nil
			s.rpcClient.Close()
			s.updateConnectedETH1(false)
			log.Debug("Context closed, exiting goroutine")
			return
		case <-s.eth1HeadTicker.C:
			// 有新的header
			head, err := s.HeaderByNumber(s.ctx, nil)
			if err != nil {
				s.pollConnectionStatus(s.ctx)
				// 不能获取最新的eth1 data
				log.WithError(err).Debug("Could not fetch latest eth1 header")
				continue
			}
			s.processBlockHeader(head)
			s.handleETH1FollowDistance()
		case <-chainstartTicker.C:
			if s.chainStartData.Chainstarted {
				chainstartTicker.Stop()
				continue
			}
			// 等待直到chain start
			s.logTillChainStart(context.Background())
		}
	}
}

// logs the current thresholds required to hit chainstart every minute.
// 记录当前的threshoulds，直到到达chainstart，每分钟一次
func (s *Service) logTillChainStart(ctx context.Context) {
	if s.chainStartData.Chainstarted {
		return
	}
	_, blockTime, err := s.retrieveBlockHashAndTime(s.ctx, big.NewInt(int64(s.latestEth1Data.LastRequestedBlock)))
	if err != nil {
		log.Error(err)
		return
	}
	valCount, genesisTime := s.currentCountAndTime(ctx, blockTime)
	valNeeded := uint64(0)
	if valCount < params.BeaconConfig().MinGenesisActiveValidatorCount {
		valNeeded = params.BeaconConfig().MinGenesisActiveValidatorCount - valCount
	}
	secondsLeft := uint64(0)
	if genesisTime < params.BeaconConfig().MinGenesisTime {
		secondsLeft = params.BeaconConfig().MinGenesisTime - genesisTime
	}

	fields := logrus.Fields{
		"Additional validators needed": valNeeded,
	}
	if secondsLeft > 0 {
		fields["Generating genesis state in"] = time.Duration(secondsLeft) * time.Second
	}

	log.WithFields(fields).Info("Currently waiting for chainstart")
}

// cacheHeadersForEth1DataVote makes sure that voting for eth1data after startup utilizes cached headers
// instead of making multiple RPC requests to the eth1 endpoint.
// cacheHeadersForEth1DataVote确保对于eth1data的voting，在启动之后，使用缓存的headers，而不是对eth1 endpoint
// 做多次RPC请求
func (s *Service) cacheHeadersForEth1DataVote(ctx context.Context) error {
	// Find the end block to request from.
	// 找到请求的end block
	end, err := s.followedBlockHeight(ctx)
	if err != nil {
		return errors.Wrap(err, "followedBlockHeight")
	}
	start, err := s.determineEarliestVotingBlock(ctx, end)
	if err != nil {
		return errors.Wrapf(err, "determineEarliestVotingBlock=%d", end)
	}
	return s.cacheBlockHeaders(start, end)
}

// Caches block headers from the desired range.
// 缓存期望范围的block headers
func (s *Service) cacheBlockHeaders(start, end uint64) error {
	batchSize := s.cfg.eth1HeaderReqLimit
	for i := start; i < end; i += batchSize {
		startReq := i
		endReq := i + batchSize
		if endReq > 0 {
			// Reduce the end request by one
			// to prevent total batch size from exceeding
			// the allotted limit.
			endReq -= 1
		}
		if endReq > end {
			endReq = end
		}
		// We call batchRequestHeaders for its header caching side-effect, so we don't need the return value.
		_, err := s.batchRequestHeaders(startReq, endReq)
		if err != nil {
			if clientTimedOutError(err) {
				// Reduce batch size as eth1 node is
				// unable to respond to the request in time.
				batchSize /= 2
				// Always have it greater than 0.
				if batchSize == 0 {
					batchSize += 1
				}

				// Reset request value
				if i > batchSize {
					i -= batchSize
				}
				continue
			}
			return errors.Wrapf(err, "cacheBlockHeaders, start=%d, end=%d", startReq, endReq)
		}
	}
	return nil
}

// Determines the earliest voting block from which to start caching all our previous headers from.
// 决定最早的voting block，从那里开始缓存我们的previous headers
func (s *Service) determineEarliestVotingBlock(ctx context.Context, followBlock uint64) (uint64, error) {
	genesisTime := s.chainStartData.GenesisTime
	currSlot := slots.CurrentSlot(genesisTime)

	// In the event genesis has not occurred yet, we just request to go back follow_distance blocks.
	if genesisTime == 0 || currSlot == 0 {
		earliestBlk := uint64(0)
		if followBlock > params.BeaconConfig().Eth1FollowDistance {
			earliestBlk = followBlock - params.BeaconConfig().Eth1FollowDistance
		}
		return earliestBlk, nil
	}
	// This should only come into play in testnets - when the chain hasn't advanced past the follow distance,
	// we don't want to consider any block before the genesis block.
	if s.latestEth1Data.BlockHeight < params.BeaconConfig().Eth1FollowDistance {
		return 0, nil
	}
	votingTime := slots.VotingPeriodStartTime(genesisTime, currSlot)
	followBackDist := 2 * params.BeaconConfig().SecondsPerETH1Block * params.BeaconConfig().Eth1FollowDistance
	if followBackDist > votingTime {
		return 0, errors.Errorf("invalid genesis time provided. %d > %d", followBackDist, votingTime)
	}
	earliestValidTime := votingTime - followBackDist
	if earliestValidTime < genesisTime {
		return 0, nil
	}
	hdr, err := s.BlockByTimestamp(ctx, earliestValidTime)
	if err != nil {
		return 0, err
	}
	return hdr.Number.Uint64(), nil
}

// initializes our service from the provided eth1data object by initializing all the relevant
// fields and data.
// initializes从提供的eth1data初始化我们的服务，通过初始化所有相关的字段和data
func (s *Service) initializeEth1Data(ctx context.Context, eth1DataInDB *ethpb.ETH1ChainData) error {
	// The node has no eth1data persisted on disk, so we exit and instead
	// request from contract logs.
	// node没有eth1data持久化在数据库中，因此我们退出并且请求contract logs
	if eth1DataInDB == nil {
		return nil
	}
	var err error
	s.depositTrie, err = trie.CreateTrieFromProto(eth1DataInDB.Trie)
	if err != nil {
		return err
	}
	s.chainStartData = eth1DataInDB.ChainstartData
	if !reflect.ValueOf(eth1DataInDB.BeaconState).IsZero() {
		s.preGenesisState, err = native.InitializeFromProtoPhase0(eth1DataInDB.BeaconState)
		if err != nil {
			return errors.Wrap(err, "Could not initialize state trie")
		}
	}
	s.latestEth1Data = eth1DataInDB.CurrentEth1Data
	numOfItems := s.depositTrie.NumOfItems()
	s.lastReceivedMerkleIndex = int64(numOfItems - 1)
	// 初始化deposit caches
	if err := s.initDepositCaches(ctx, eth1DataInDB.DepositContainers); err != nil {
		return errors.Wrap(err, "could not initialize caches")
	}
	return nil
}

// Validates that all deposit containers are valid and have their relevant indices
// in order.
// 校验所有的deposit containers都是合法的并且它们相关的索引都是有序的
func validateDepositContainers(ctrs []*ethpb.DepositContainer) bool {
	ctrLen := len(ctrs)
	// Exit for empty containers.
	if ctrLen == 0 {
		return true
	}
	// Sort deposits in ascending order.
	sort.Slice(ctrs, func(i, j int) bool {
		return ctrs[i].Index < ctrs[j].Index
	})
	startIndex := int64(0)
	for _, c := range ctrs {
		if c.Index != startIndex {
			log.Info("Recovering missing deposit containers, node is re-requesting missing deposit data")
			return false
		}
		startIndex++
	}
	return true
}

// Validates the current powchain data is saved and makes sure that any
// embedded genesis state is correctly accounted for.
// 校验当前的powchain data已经被保存了并且确保任何内置的genesis state被正确解释了
func (s *Service) ensureValidPowchainData(ctx context.Context) error {
	genState, err := s.cfg.beaconDB.GenesisState(ctx)
	if err != nil {
		return err
	}
	// Exit early if no genesis state is saved.
	// 如果没有保存genesis state，尽早返回
	if genState == nil || genState.IsNil() {
		return nil
	}
	eth1Data, err := s.cfg.beaconDB.ExecutionChainData(ctx)
	if err != nil {
		return errors.Wrap(err, "unable to retrieve eth1 data")
	}
	if eth1Data == nil || !eth1Data.ChainstartData.Chainstarted || !validateDepositContainers(eth1Data.DepositContainers) {
		pbState, err := native.ProtobufBeaconStatePhase0(s.preGenesisState.ToProtoUnsafe())
		if err != nil {
			return err
		}
		// 构建chain start data
		s.chainStartData = &ethpb.ChainStartData{
			Chainstarted:       true,
			GenesisTime:        genState.GenesisTime(),
			GenesisBlock:       0,
			Eth1Data:           genState.Eth1Data(),
			ChainstartDeposits: make([]*ethpb.Deposit, 0),
		}
		eth1Data = &ethpb.ETH1ChainData{
			CurrentEth1Data:   s.latestEth1Data,
			ChainstartData:    s.chainStartData,
			BeaconState:       pbState,
			Trie:              s.depositTrie.ToProto(),
			DepositContainers: s.cfg.depositCache.AllDepositContainers(ctx),
		}
		// 创建eth1 data并且保存
		return s.cfg.beaconDB.SaveExecutionChainData(ctx, eth1Data)
	}
	return nil
}

func dedupEndpoints(endpoints []string) []string {
	selectionMap := make(map[string]bool)
	newEndpoints := make([]string, 0, len(endpoints))
	for _, point := range endpoints {
		if selectionMap[point] {
			continue
		}
		newEndpoints = append(newEndpoints, point)
		selectionMap[point] = true
	}
	return newEndpoints
}
