// Package blockchain defines the life-cycle of the blockchain at the core of
// Ethereum, including processing of new blocks and attestations using proof of stake.
// blockchain定义了Ethereum核心的blockchain的生命周期，包括使用pos处理新的blocks以及attestations
package blockchain

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/async/event"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/cache"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/cache/depositcache"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed"
	statefeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/state"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/execution"
	f "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice"
	forkchoicetypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice/types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/attestations"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/blstoexec"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/slashings"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/voluntaryexits"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/p2p"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stategen"
	"github.com/prysmaticlabs/prysm/v3/config/features"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	prysmTime "github.com/prysmaticlabs/prysm/v3/time"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"go.opencensus.io/trace"
)

// Service represents a service that handles the internal
// logic of managing the full PoS beacon chain.
// Service代表一个服务，处理关于管理完整的PoS beacon chain的内部逻辑
type Service struct {
	cfg         *config
	ctx         context.Context
	cancel      context.CancelFunc
	genesisTime time.Time
	head        *head
	headLock    sync.RWMutex
	// genesis root或者weak subjectivity checkpoint root，依赖于node如何初始化
	originBlockRoot         [32]byte // genesis root, or weak subjectivity checkpoint root, depending on how the node is initialized
	nextEpochBoundarySlot   types.Slot
	boundaryRoots           [][32]byte
	checkpointStateCache    *cache.CheckpointStateCache
	initSyncBlocks          map[[32]byte]interfaces.SignedBeaconBlock
	initSyncBlocksLock      sync.RWMutex
	justifiedBalances       *stateBalanceCache
	wsVerifier              *WeakSubjectivityVerifier
	processAttestationsLock sync.Mutex
}

// config options for the service.
type config struct {
	BeaconBlockBuf          int
	ChainStartFetcher       execution.ChainStartFetcher
	BeaconDB                db.HeadAccessDatabase
	DepositCache            *depositcache.DepositCache
	ProposerSlotIndexCache  *cache.ProposerPayloadIDsCache
	AttPool                 attestations.Pool
	ExitPool                voluntaryexits.PoolManager
	SlashingPool            slashings.PoolManager
	BLSToExecPool           blstoexec.PoolManager
	P2p                     p2p.Broadcaster
	MaxRoutines             int
	StateNotifier           statefeed.Notifier
	ForkChoiceStore         f.ForkChoicer
	AttService              *attestations.Service
	StateGen                *stategen.State
	SlasherAttestationsFeed *event.Feed
	WeakSubjectivityCheckpt *ethpb.Checkpoint
	BlockFetcher            execution.POWBlockFetcher
	FinalizedStateAtStartUp state.BeaconState
	ExecutionEngineCaller   execution.EngineCaller
}

// NewService instantiates a new block service instance that will
// be registered into a running beacon node.
// NewService实例化一个新的block service实例，会在一个运行的beacon node注册
func NewService(ctx context.Context, opts ...Option) (*Service, error) {
	ctx, cancel := context.WithCancel(ctx)
	srv := &Service{
		ctx:                  ctx,
		cancel:               cancel,
		boundaryRoots:        [][32]byte{},
		checkpointStateCache: cache.NewCheckpointStateCache(),
		initSyncBlocks:       make(map[[32]byte]interfaces.SignedBeaconBlock),
		cfg:                  &config{},
	}
	// 应用opts
	for _, opt := range opts {
		if err := opt(srv); err != nil {
			return nil, err
		}
	}
	var err error
	if srv.justifiedBalances == nil {
		// 构建一个新的justified balances
		srv.justifiedBalances, err = newStateBalanceCache(srv.cfg.StateGen)
		if err != nil {
			return nil, err
		}
	}
	srv.wsVerifier, err = NewWeakSubjectivityVerifier(srv.cfg.WeakSubjectivityCheckpt, srv.cfg.BeaconDB)
	if err != nil {
		return nil, err
	}
	return srv, nil
}

// Start a blockchain service's main event loop.
// 启动blockchain service的主事件循环
func (s *Service) Start() {
	saved := s.cfg.FinalizedStateAtStartUp

	if saved != nil && !saved.IsNil() {
		if err := s.StartFromSavedState(saved); err != nil {
			log.Fatal(err)
		}
	} else {
		if err := s.startFromExecutionChain(); err != nil {
			log.Fatal(err)
		}
	}
	s.spawnProcessAttestationsRoutine(s.cfg.StateNotifier.StateFeed())
	s.fillMissingPayloadIDRoutine(s.ctx, s.cfg.StateNotifier.StateFeed())
}

// Stop the blockchain service's main event loop and associated goroutines.
// 停止blockchain service的main event loop以及相关的goroutines
func (s *Service) Stop() error {
	defer s.cancel()

	// lock before accessing s.head, s.head.state, s.head.state.FinalizedCheckpoint().Root
	// 在访问s.head，s.head.state，s.head.state.FinalizedCheckpoint().Root前先锁定
	s.headLock.RLock()
	if s.cfg.StateGen != nil && s.head != nil && s.head.state != nil {
		r := s.head.state.FinalizedCheckpoint().Root
		s.headLock.RUnlock()
		// Save the last finalized state so that starting up in the following run will be much faster.
		// 保存最后的finalized state，这样在后续的运行会更快
		if err := s.cfg.StateGen.ForceCheckpoint(s.ctx, r); err != nil {
			return err
		}
	} else {
		s.headLock.RUnlock()
	}
	// Save initial sync cached blocks to the DB before stop.
	// 在stop之前，保存初始的sync cached blocks
	return s.cfg.BeaconDB.SaveBlocks(s.ctx, s.getInitSyncBlocks())
}

// Status always returns nil unless there is an error condition that causes
// this service to be unhealthy.
func (s *Service) Status() error {
	optimistic, err := s.IsOptimistic(s.ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check if service is optimistic")
	}
	if optimistic {
		return errors.New("service is optimistic, and only limited service functionality is provided " +
			"please check if execution layer is fully synced")
	}

	if s.originBlockRoot == params.BeaconConfig().ZeroHash {
		return errors.New("genesis state has not been created")
	}
	if runtime.NumGoroutine() > s.cfg.MaxRoutines {
		return fmt.Errorf("too many goroutines (%d)", runtime.NumGoroutine())
	}
	return nil
}

// StartFromSavedState initializes the blockchain using a previously saved finalized checkpoint.
// StartFromSavedState使用之前保存的finalized checkpoint初始化blockchain
func (s *Service) StartFromSavedState(saved state.BeaconState) error {
	log.Info("Blockchain data already exists in DB, initializing...")
	s.genesisTime = time.Unix(int64(saved.GenesisTime()), 0) // lint:ignore uintcast -- Genesis time will not exceed int64 in your lifetime.
	// 设置genesis time
	s.cfg.AttService.SetGenesisTime(saved.GenesisTime())

	originRoot, err := s.originRootFromSavedState(s.ctx)
	if err != nil {
		return err
	}
	s.originBlockRoot = originRoot

	// 从DB对head进行初始化
	if err := s.initializeHeadFromDB(s.ctx); err != nil {
		return errors.Wrap(err, "could not set up chain info")
	}
	spawnCountdownIfPreGenesis(s.ctx, s.genesisTime, s.cfg.BeaconDB)

	justified, err := s.cfg.BeaconDB.JustifiedCheckpoint(s.ctx)
	if err != nil {
		return errors.Wrap(err, "could not get justified checkpoint")
	}
	if justified == nil {
		return errNilJustifiedCheckpoint
	}
	// 从DB中获取justified和finalized checkpoint
	finalized, err := s.cfg.BeaconDB.FinalizedCheckpoint(s.ctx)
	if err != nil {
		return errors.Wrap(err, "could not get finalized checkpoint")
	}
	if finalized == nil {
		return errNilFinalizedCheckpoint
	}

	fRoot := s.ensureRootNotZeros(bytesutil.ToBytes32(finalized.Root))
	// 更新fork choice store中的两个checkpoint
	if err := s.cfg.ForkChoiceStore.UpdateJustifiedCheckpoint(&forkchoicetypes.Checkpoint{Epoch: justified.Epoch,
		Root: bytesutil.ToBytes32(justified.Root)}); err != nil {
		return errors.Wrap(err, "could not update forkchoice's justified checkpoint")
	}
	if err := s.cfg.ForkChoiceStore.UpdateFinalizedCheckpoint(&forkchoicetypes.Checkpoint{Epoch: finalized.Epoch,
		Root: bytesutil.ToBytes32(finalized.Root)}); err != nil {
		return errors.Wrap(err, "could not update forkchoice's finalized checkpoint")
	}
	s.cfg.ForkChoiceStore.SetGenesisTime(uint64(s.genesisTime.Unix()))

	st, err := s.cfg.StateGen.StateByRoot(s.ctx, fRoot)
	if err != nil {
		// 不能获取finalized checkpoint state
		return errors.Wrap(err, "could not get finalized checkpoint state")
	}
	if err := s.cfg.ForkChoiceStore.InsertNode(s.ctx, st, fRoot); err != nil {
		return errors.Wrap(err, "could not insert finalized block to forkchoice")
	}
	if !features.Get().EnableStartOptimistic {
		lastValidatedCheckpoint, err := s.cfg.BeaconDB.LastValidatedCheckpoint(s.ctx)
		if err != nil {
			return errors.Wrap(err, "could not get last validated checkpoint")
		}
		if bytes.Equal(finalized.Root, lastValidatedCheckpoint.Root) {
			if err := s.cfg.ForkChoiceStore.SetOptimisticToValid(s.ctx, fRoot); err != nil {
				return errors.Wrap(err, "could not set finalized block as validated")
			}
		}
	}
	// not attempting to save initial sync blocks here, because there shouldn't be any until
	// after the statefeed.Initialized event is fired (below)
	// 不要试着在这里保存initial sync blocks，因为这里不应该有，直到statefeed.Initialized事件被触发之后
	if err := s.wsVerifier.VerifyWeakSubjectivity(s.ctx, finalized.Epoch); err != nil {
		// Exit run time if the node failed to verify weak subjectivity checkpoint.
		// 退出runtime，如果node校验weak subjectivity checkpoint失败
		return errors.Wrap(err, "could not verify initial checkpoint provided for chain sync")
	}

	s.cfg.StateNotifier.StateFeed().Send(&feed.Event{
		Type: statefeed.Initialized,
		Data: &statefeed.InitializedData{
			StartTime:             s.genesisTime,
			GenesisValidatorsRoot: saved.GenesisValidatorsRoot(),
		},
	})

	return nil
}

func (s *Service) originRootFromSavedState(ctx context.Context) ([32]byte, error) {
	// first check if we have started from checkpoint sync and have a root
	// 首先检查我们是否从checkpoint sync开始并且有一个root
	originRoot, err := s.cfg.BeaconDB.OriginCheckpointBlockRoot(ctx)
	if err == nil {
		return originRoot, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		// 不能从db中获取checkpoint sync chain origin data
		return originRoot, errors.Wrap(err, "could not retrieve checkpoint sync chain origin data from db")
	}

	// we got here because OriginCheckpointBlockRoot gave us an ErrNotFound. this means the node was started from a genesis state,
	// so we should have a value for GenesisBlock
	// 我们到达这里，因为OriginCheckpointBlockRoot给了我们一个ErrNotFound，这意味着node从一个genesis state开始
	// 因此我们应该有一个GenesisBlock的值
	genesisBlock, err := s.cfg.BeaconDB.GenesisBlock(ctx)
	if err != nil {
		return originRoot, errors.Wrap(err, "could not get genesis block from db")
	}
	if err := blocks.BeaconBlockIsNil(genesisBlock); err != nil {
		return originRoot, err
	}
	genesisBlkRoot, err := genesisBlock.Block().HashTreeRoot()
	if err != nil {
		return genesisBlkRoot, errors.Wrap(err, "could not get signing root of genesis block")
	}
	return genesisBlkRoot, nil
}

// initializeHeadFromDB uses the finalized checkpoint and head block found in the database to set the current head.
// Note that this may block until stategen replays blocks between the finalized and head blocks
// if the head sync flag was specified and the gap between the finalized and head blocks is at least 128 epochs long.
// initializeHeadFromDB使用finalized checkpoint以及从数据库中找到的head block来设置当前的head
// 注意这可能会block直到stategen在finalized和head blocks之间重放，如果指定了head sync flag，finalized和head blocks之间的gap
// 至少有128个epochs
func (s *Service) initializeHeadFromDB(ctx context.Context) error {
	finalized, err := s.cfg.BeaconDB.FinalizedCheckpoint(ctx)
	if err != nil {
		return errors.Wrap(err, "could not get finalized checkpoint from db")
	}
	if finalized == nil {
		// This should never happen. At chain start, the finalized checkpoint
		// would be the genesis state and block.
		// 不会存在db中没有finalized epoch的情况，在chain start的时候，finalized checkpoint
		// 会是genesis state以及block
		return errors.New("no finalized epoch in the database")
	}
	finalizedRoot := s.ensureRootNotZeros(bytesutil.ToBytes32(finalized.Root))
	var finalizedState state.BeaconState

	finalizedState, err = s.cfg.StateGen.Resume(ctx, s.cfg.FinalizedStateAtStartUp)
	if err != nil {
		// 不能从db中获取finalize state
		return errors.Wrap(err, "could not get finalized state from db")
	}

	if finalizedState == nil || finalizedState.IsNil() {
		return errors.New("finalized state can't be nil")
	}

	finalizedBlock, err := s.getBlock(ctx, finalizedRoot)
	if err != nil {
		// 不能获取finalized block
		return errors.Wrap(err, "could not get finalized block")
	}
	if err := s.setHead(finalizedRoot, finalizedBlock, finalizedState); err != nil {
		return errors.Wrap(err, "could not set head")
	}

	return nil
}

func (s *Service) startFromExecutionChain() error {
	// 等待到达validator deposit threshold，来启动beacon chain...
	log.Info("Waiting to reach the validator deposit threshold to start the beacon chain...")
	if s.cfg.ChainStartFetcher == nil {
		return errors.New("not configured execution chain")
	}
	go func() {
		stateChannel := make(chan *feed.Event, 1)
		stateSub := s.cfg.StateNotifier.StateFeed().Subscribe(stateChannel)
		defer stateSub.Unsubscribe()
		for {
			select {
			case e := <-stateChannel:
				// 等待chain started event
				if e.Type == statefeed.ChainStarted {
					data, ok := e.Data.(*statefeed.ChainStartedData)
					if !ok {
						log.Error("event data is not type *statefeed.ChainStartedData")
						return
					}
					log.WithField("starttime", data.StartTime).Debug("Received chain start event")
					// 收到chain start
					s.onExecutionChainStart(s.ctx, data.StartTime)
					return
				}
			case <-s.ctx.Done():
				log.Debug("Context closed, exiting goroutine")
				return
			case err := <-stateSub.Err():
				log.WithError(err).Error("Subscription to state notifier failed")
				return
			}
		}
	}()

	return nil
}

// onExecutionChainStart initializes a series of deposits from the ChainStart deposits in the eth1
// deposit contract, initializes the beacon chain's state, and kicks off the beacon chain.
// onExecutionChainStart从ChainStart deposits初始化一系列的deposits，在eth1 deposit contract
// 初始化beacon chain的state，并且开始beacon chain
func (s *Service) onExecutionChainStart(ctx context.Context, genesisTime time.Time) {
	preGenesisState := s.cfg.ChainStartFetcher.PreGenesisState()
	initializedState, err := s.initializeBeaconChain(ctx, genesisTime, preGenesisState, s.cfg.ChainStartFetcher.ChainStartEth1Data())
	if err != nil {
		log.WithError(err).Fatal("Could not initialize beacon chain")
	}
	// We start a counter to genesis, if needed.
	// 我们启动到genesis的计数，如果需要的话
	gRoot, err := initializedState.HashTreeRoot(s.ctx)
	if err != nil {
		log.WithError(err).Fatal("Could not hash tree root genesis state")
	}
	go slots.CountdownToGenesis(ctx, genesisTime, uint64(initializedState.NumValidators()), gRoot)

	// We send out a state initialized event to the rest of the services
	// running in the beacon node.
	// 我们发送一个state initialized event到在beacon chain中运行的其余的services
	s.cfg.StateNotifier.StateFeed().Send(&feed.Event{
		Type: statefeed.Initialized,
		Data: &statefeed.InitializedData{
			StartTime:             genesisTime,
			GenesisValidatorsRoot: initializedState.GenesisValidatorsRoot(),
		},
	})
}

// initializes the state and genesis block of the beacon chain to persistent storage
// based on a genesis timestamp value obtained from the ChainStart event emitted
// by the ETH1.0 Deposit Contract and the POWChain service of the node.
// 初始化state以及beacon chain的genesis block到持久化存储，基于从ChainStart event中
// 获取的genesis timestamp，通过ETH1.0 Deposit Contract以及这个节点的POWChain service
func (s *Service) initializeBeaconChain(
	ctx context.Context,
	genesisTime time.Time,
	preGenesisState state.BeaconState,
	eth1data *ethpb.Eth1Data) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "beacon-chain.Service.initializeBeaconChain")
	defer span.End()
	s.genesisTime = genesisTime
	unixTime := uint64(genesisTime.Unix())

	genesisState, err := transition.OptimizedGenesisBeaconState(unixTime, preGenesisState, eth1data)
	if err != nil {
		return nil, errors.Wrap(err, "could not initialize genesis state")
	}

	if err := s.saveGenesisData(ctx, genesisState); err != nil {
		return nil, errors.Wrap(err, "could not save genesis data")
	}

	log.Info("Initialized beacon chain genesis state")

	// Clear out all pre-genesis data now that the state is initialized.
	// 清理所有的pre-genesis data，现在state已经初始化完成
	s.cfg.ChainStartFetcher.ClearPreGenesisData()

	// Update committee shuffled indices for genesis epoch.
	// 更新committee shuffled indices，对于genesis epoch
	if err := helpers.UpdateCommitteeCache(ctx, genesisState, 0); err != nil {
		return nil, err
	}
	if err := helpers.UpdateProposerIndicesInCache(ctx, genesisState); err != nil {
		return nil, err
	}

	s.cfg.AttService.SetGenesisTime(genesisState.GenesisTime())

	return genesisState, nil
}

// This gets called when beacon chain is first initialized to save genesis data (state, block, and more) in db.
// 这在beacon chain第一次被初始化的时候调用，来保存genesis data（state, block以及更多）
func (s *Service) saveGenesisData(ctx context.Context, genesisState state.BeaconState) error {
	if err := s.cfg.BeaconDB.SaveGenesisData(ctx, genesisState); err != nil {
		return errors.Wrap(err, "could not save genesis data")
	}
	genesisBlk, err := s.cfg.BeaconDB.GenesisBlock(ctx)
	if err != nil || genesisBlk == nil || genesisBlk.IsNil() {
		return fmt.Errorf("could not load genesis block: %v", err)
	}
	genesisBlkRoot, err := genesisBlk.Block().HashTreeRoot()
	if err != nil {
		return errors.Wrap(err, "could not get genesis block root")
	}

	s.originBlockRoot = genesisBlkRoot
	// 设置finalized state
	s.cfg.StateGen.SaveFinalizedState(0 /*slot*/, genesisBlkRoot, genesisState)

	if err := s.cfg.ForkChoiceStore.InsertNode(ctx, genesisState, genesisBlkRoot); err != nil {
		// 不能对fork choice处理genesis block
		log.WithError(err).Fatal("Could not process genesis block for fork choice")
	}
	s.cfg.ForkChoiceStore.SetOriginRoot(genesisBlkRoot)
	// Set genesis as fully validated
	// 设置genesis为完全校验过的
	if err := s.cfg.ForkChoiceStore.SetOptimisticToValid(ctx, genesisBlkRoot); err != nil {
		return errors.Wrap(err, "Could not set optimistic status of genesis block to false")
	}
	s.cfg.ForkChoiceStore.SetGenesisTime(uint64(s.genesisTime.Unix()))

	if err := s.setHead(genesisBlkRoot, genesisBlk, genesisState); err != nil {
		// 设置head
		log.WithError(err).Fatal("Could not set head")
	}
	return nil
}

// This returns true if block has been processed before. Two ways to verify the block has been processed:
// 1.) Check fork choice store.
// 2.) Check DB.
// Checking 1.) is ten times faster than checking 2.)
// 返回true，如果block之前已经被处理过了，两种方式用于校验block已经被处理过了：
// 1). 检查fork choice store，2) 检查DB，方法一比方法二快十倍
func (s *Service) hasBlock(ctx context.Context, root [32]byte) bool {
	if s.cfg.ForkChoiceStore.HasNode(root) {
		return true
	}

	return s.cfg.BeaconDB.HasBlock(ctx, root)
}

func spawnCountdownIfPreGenesis(ctx context.Context, genesisTime time.Time, db db.HeadAccessDatabase) {
	currentTime := prysmTime.Now()
	if currentTime.After(genesisTime) {
		return
	}

	gState, err := db.GenesisState(ctx)
	if err != nil {
		log.WithError(err).Fatal("Could not retrieve genesis state")
	}
	gRoot, err := gState.HashTreeRoot(ctx)
	if err != nil {
		log.WithError(err).Fatal("Could not hash tree root genesis state")
	}
	go slots.CountdownToGenesis(ctx, genesisTime, uint64(gState.NumValidators()), gRoot)
}
