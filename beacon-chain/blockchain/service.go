// Package blockchain defines the life-cycle of the blockchain at the core of
// Ethereum, including processing of new blocks and attestations using proof of stake.
package blockchain

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/async/event"
	"github.com/prysmaticlabs/prysm/beacon-chain/cache"
	"github.com/prysmaticlabs/prysm/beacon-chain/cache/depositcache"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/feed"
	statefeed "github.com/prysmaticlabs/prysm/beacon-chain/core/feed/state"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/beacon-chain/db"
	f "github.com/prysmaticlabs/prysm/beacon-chain/forkchoice"
	doublylinkedtree "github.com/prysmaticlabs/prysm/beacon-chain/forkchoice/doubly-linked-tree"
	"github.com/prysmaticlabs/prysm/beacon-chain/forkchoice/protoarray"
	forkchoicetypes "github.com/prysmaticlabs/prysm/beacon-chain/forkchoice/types"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/attestations"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/slashings"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/voluntaryexits"
	"github.com/prysmaticlabs/prysm/beacon-chain/p2p"
	"github.com/prysmaticlabs/prysm/beacon-chain/powchain"
	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/beacon-chain/state/stategen"
	"github.com/prysmaticlabs/prysm/cmd/beacon-chain/flags"
	"github.com/prysmaticlabs/prysm/config/features"
	"github.com/prysmaticlabs/prysm/config/params"
	"github.com/prysmaticlabs/prysm/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/consensus-types/wrapper"
	"github.com/prysmaticlabs/prysm/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	prysmTime "github.com/prysmaticlabs/prysm/time"
	"github.com/prysmaticlabs/prysm/time/slots"
	"go.opencensus.io/trace"
)

// headSyncMinEpochsAfterCheckpoint defines how many epochs should elapse after known finalization
// checkpoint for head sync to be triggered.
// headSyncMinEpochsAfterCheckpoint定义了应该过多少epochs，在已知的finalization checkpoint之后
// 对于head sync去触发
const headSyncMinEpochsAfterCheckpoint = 128

// Service represents a service that handles the internal
// logic of managing the full PoS beacon chain.
// Service代表一个service，处理管理完整的PoS beacon chain的内部逻辑
type Service struct {
	cfg         *config
	ctx         context.Context
	cancel      context.CancelFunc
	genesisTime time.Time
	head        *head
	headLock    sync.RWMutex
	// genesis root，或者weak subjective checkpoint root，取决于node是如何初始化的
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
	ChainStartFetcher       powchain.ChainStartFetcher
	BeaconDB                db.HeadAccessDatabase
	DepositCache            *depositcache.DepositCache
	ProposerSlotIndexCache  *cache.ProposerPayloadIDsCache
	AttPool                 attestations.Pool
	ExitPool                voluntaryexits.PoolManager
	SlashingPool            slashings.PoolManager
	P2p                     p2p.Broadcaster
	MaxRoutines             int
	StateNotifier           statefeed.Notifier
	ForkChoiceStore         f.ForkChoicer
	AttService              *attestations.Service
	StateGen                *stategen.State
	SlasherAttestationsFeed *event.Feed
	WeakSubjectivityCheckpt *ethpb.Checkpoint
	BlockFetcher            powchain.POWBlockFetcher
	FinalizedStateAtStartUp state.BeaconState
	ExecutionEngineCaller   powchain.EngineCaller
}

// NewService instantiates a new block service instance that will
// be registered into a running beacon node.
// NewService初始化一个新的block service实例，会注册到一个正在运行的beacon node
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
	for _, opt := range opts {
		if err := opt(srv); err != nil {
			return nil, err
		}
	}
	var err error
	if srv.justifiedBalances == nil {
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
// Start启动一个blockchain service的main event loop
func (s *Service) Start() {
	// 如果包含了finalized state
	saved := s.cfg.FinalizedStateAtStartUp

	if saved != nil && !saved.IsNil() {
		if err := s.StartFromSavedState(saved); err != nil {
			log.Fatal(err)
		}
	} else {
		// 如果没有之前保存的数据，则从POW Chain开始
		if err := s.startFromPOWChain(); err != nil {
			log.Fatal(err)
		}
	}
	s.spawnProcessAttestationsRoutine(s.cfg.StateNotifier.StateFeed())
	s.fillMissingPayloadIDRoutine(s.ctx, s.cfg.StateNotifier.StateFeed())
}

// Stop the blockchain service's main event loop and associated goroutines.
// 停止blockchain service的main event loop以及相关的goroutine
func (s *Service) Stop() error {
	defer s.cancel()

	// lock before accessing s.head, s.head.state, s.head.state.FinalizedCheckpoint().Root
	// 在访问s.head, s.head.state, s.head.state.FinalizedCheckpoint().Root之前先锁住
	s.headLock.RLock()
	if s.cfg.StateGen != nil && s.head != nil && s.head.state != nil {
		r := s.head.state.FinalizedCheckpoint().Root
		s.headLock.RUnlock()
		// Save the last finalized state so that starting up in the following run will be much faster.
		// 保存最新的finalized state，这样后面再启动的时候会更快
		if err := s.cfg.StateGen.ForceCheckpoint(s.ctx, r); err != nil {
			return err
		}
	} else {
		s.headLock.RUnlock()
	}
	// Save initial sync cached blocks to the DB before stop.
	// 保存初始化的sync cached blocks到DB，在停止之前
	return s.cfg.BeaconDB.SaveBlocks(s.ctx, s.getInitSyncBlocks())
}

// Status always returns nil unless there is an error condition that causes
// this service to be unhealthy.
// Status总是返回nil，除非有error condition，导致service变为unhealthy
func (s *Service) Status() error {
	if s.originBlockRoot == params.BeaconConfig().ZeroHash {
		return errors.New("genesis state has not been created")
	}
	if runtime.NumGoroutine() > s.cfg.MaxRoutines {
		// goroutine过多？
		return fmt.Errorf("too many goroutines (%d)", runtime.NumGoroutine())
	}
	return nil
}

// StartFromSavedState initializes the blockchain using a previously saved finalized checkpoint.
// StartFromSavedState初始化blockchain，使用之前保存的finalized checkpoint
func (s *Service) StartFromSavedState(saved state.BeaconState) error {
	// Blockchain data已经存在于DB中，初始化...
	log.Info("Blockchain data already exists in DB, initializing...")
	s.genesisTime = time.Unix(int64(saved.GenesisTime()), 0) // lint:ignore uintcast -- Genesis time will not exceed int64 in your lifetime.
	s.cfg.AttService.SetGenesisTime(saved.GenesisTime())

	originRoot, err := s.originRootFromSavedState(s.ctx)
	if err != nil {
		return err
	}
	s.originBlockRoot = originRoot

	// 从DB中初始化head
	if err := s.initializeHeadFromDB(s.ctx); err != nil {
		return errors.Wrap(err, "could not set up chain info")
	}
	spawnCountdownIfPreGenesis(s.ctx, s.genesisTime, s.cfg.BeaconDB)

	// 获取justified checkpoint
	justified, err := s.cfg.BeaconDB.JustifiedCheckpoint(s.ctx)
	if err != nil {
		return errors.Wrap(err, "could not get justified checkpoint")
	}
	if justified == nil {
		return errNilJustifiedCheckpoint
	}

	// 获取finalized checkpoint
	finalized, err := s.cfg.BeaconDB.FinalizedCheckpoint(s.ctx)
	if err != nil {
		return errors.Wrap(err, "could not get finalized checkpoint")
	}
	if finalized == nil {
		return errNilFinalizedCheckpoint
	}

	var forkChoicer f.ForkChoicer
	fRoot := s.ensureRootNotZeros(bytesutil.ToBytes32(finalized.Root))
	if features.Get().EnableForkChoiceDoublyLinkedTree {
		forkChoicer = doublylinkedtree.New()
	} else {
		forkChoicer = protoarray.New()
	}
	s.cfg.ForkChoiceStore = forkChoicer
	if err := forkChoicer.UpdateJustifiedCheckpoint(&forkchoicetypes.Checkpoint{Epoch: justified.Epoch,
		Root: bytesutil.ToBytes32(justified.Root)}); err != nil {
		return errors.Wrap(err, "could not update forkchoice's justified checkpoint")
	}
	if err := forkChoicer.UpdateFinalizedCheckpoint(&forkchoicetypes.Checkpoint{Epoch: finalized.Epoch,
		Root: bytesutil.ToBytes32(finalized.Root)}); err != nil {
		return errors.Wrap(err, "could not update forkchoice's finalized checkpoint")
	}
	forkChoicer.SetGenesisTime(uint64(s.genesisTime.Unix()))

	st, err := s.cfg.StateGen.StateByRoot(s.ctx, fRoot)
	if err != nil {
		return errors.Wrap(err, "could not get finalized checkpoint state")
	}
	if err := forkChoicer.InsertNode(s.ctx, st, fRoot); err != nil {
		// 不能插入finalized block到forkchoice
		return errors.Wrap(err, "could not insert finalized block to forkchoice")
	}

	lastValidatedCheckpoint, err := s.cfg.BeaconDB.LastValidatedCheckpoint(s.ctx)
	if err != nil {
		return errors.Wrap(err, "could not get last validated checkpoint")
	}
	if bytes.Equal(finalized.Root, lastValidatedCheckpoint.Root) {
		if err := forkChoicer.SetOptimisticToValid(s.ctx, fRoot); err != nil {
			return errors.Wrap(err, "could not set finalized block as validated")
		}
	}
	// not attempting to save initial sync blocks here, because there shouldn't be any until
	// after the statefeed.Initialized event is fired (below)
	if err := s.wsVerifier.VerifyWeakSubjectivity(s.ctx, finalized.Epoch); err != nil {
		// Exit run time if the node failed to verify weak subjectivity checkpoint.
		// 退出runtime，如果node不能校验weak subjective checkpoint
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
		return originRoot, errors.Wrap(err, "could not retrieve checkpoint sync chain origin data from db")
	}

	// we got here because OriginCheckpointBlockRoot gave us an ErrNotFound. this means the node was started from a genesis state,
	// so we should have a value for GenesisBlock
	// 我们到达这里，因为OriginCheckpointBlockRoot给我们一个ErrNotFound，这意味着node从genesis state开始
	// 因此我们应该对于GenesisBlock有一个值
	genesisBlock, err := s.cfg.BeaconDB.GenesisBlock(ctx)
	if err != nil {
		return originRoot, errors.Wrap(err, "could not get genesis block from db")
	}
	if err := wrapper.BeaconBlockIsNil(genesisBlock); err != nil {
		return originRoot, err
	}
	genesisBlkRoot, err := genesisBlock.Block().HashTreeRoot()
	if err != nil {
		return genesisBlkRoot, errors.Wrap(err, "could not get signing root of genesis block")
	}
	return genesisBlkRoot, nil
}

// initializeHeadFromDB uses the finalized checkpoint and head block found in the database to set the current head.
// initializeHeadFromDB使用finalized checkpoint以及在db中找到的head block，来设置正确的head
// Note that this may block until stategen replays blocks between the finalized and head blocks
// 注意这可能会阻塞直到stategen要在finalized和head blocks间重放blocks
// if the head sync flag was specified and the gap between the finalized and head blocks is at least 128 epochs long.
// 如果指定了head sync flag，finalized以及head blocks的gap至少有128个epochs
func (s *Service) initializeHeadFromDB(ctx context.Context) error {
	finalized, err := s.cfg.BeaconDB.FinalizedCheckpoint(ctx)
	if err != nil {
		return errors.Wrap(err, "could not get finalized checkpoint from db")
	}
	if finalized == nil {
		// This should never happen. At chain start, the finalized checkpoint
		// would be the genesis state and block.
		// 这不应该发生，在chain start，finalized checkpoint应该为genesis state以及block
		return errors.New("no finalized epoch in the database")
	}
	finalizedRoot := s.ensureRootNotZeros(bytesutil.ToBytes32(finalized.Root))
	var finalizedState state.BeaconState

	finalizedState, err = s.cfg.StateGen.Resume(ctx, s.cfg.FinalizedStateAtStartUp)
	if err != nil {
		return errors.Wrap(err, "could not get finalized state from db")
	}

	if flags.Get().HeadSync {
		headBlock, err := s.cfg.BeaconDB.HeadBlock(ctx)
		if err != nil {
			return errors.Wrap(err, "could not retrieve head block")
		}
		headEpoch := slots.ToEpoch(headBlock.Block().Slot())
		var epochsSinceFinality types.Epoch
		if headEpoch > finalized.Epoch {
			epochsSinceFinality = headEpoch - finalized.Epoch
		}
		// Head sync when node is far enough beyond known finalized epoch,
		// this becomes really useful during long period of non-finality.
		// 当node在已知的finalized epoch很远之后，进行head sync
		// 这很有用，在长期的non-finality
		if epochsSinceFinality >= headSyncMinEpochsAfterCheckpoint {
			headRoot, err := headBlock.Block().HashTreeRoot()
			if err != nil {
				return errors.Wrap(err, "could not hash head block")
			}
			finalizedState, err := s.cfg.StateGen.Resume(ctx, s.cfg.FinalizedStateAtStartUp)
			if err != nil {
				return errors.Wrap(err, "could not get finalized state from db")
			}
			log.Infof("Regenerating state from the last checkpoint at slot %d to current head slot of %d."+
				"This process may take a while, please wait.", finalizedState.Slot(), headBlock.Block().Slot())
			headState, err := s.cfg.StateGen.StateByRoot(ctx, headRoot)
			if err != nil {
				return errors.Wrap(err, "could not retrieve head state")
			}
			s.setHead(headRoot, headBlock, headState)
			return nil
		} else {
			log.Warnf("Finalized checkpoint at slot %d is too close to the current head slot, "+
				"resetting head from the checkpoint ('--%s' flag is ignored).",
				finalizedState.Slot(), flags.HeadSync.Name)
		}
	}
	if finalizedState == nil || finalizedState.IsNil() {
		return errors.New("finalized state can't be nil")
	}

	finalizedBlock, err := s.getBlock(ctx, finalizedRoot)
	if err != nil {
		return errors.Wrap(err, "could not get finalized block")
	}
	// 设置head
	s.setHead(finalizedRoot, finalizedBlock, finalizedState)

	return nil
}

func (s *Service) startFromPOWChain() error {
	// 等待到达validator deposit threshold来启动beacon chain
	log.Info("Waiting to reach the validator deposit threshold to start the beacon chain...")
	if s.cfg.ChainStartFetcher == nil {
		return errors.New("not configured web3Service for POW chain")
	}
	go func() {
		stateChannel := make(chan *feed.Event, 1)
		// 它也要等待接收state更新
		stateSub := s.cfg.StateNotifier.StateFeed().Subscribe(stateChannel)
		defer stateSub.Unsubscribe()
		for {
			select {
			case e := <-stateChannel:
				if e.Type == statefeed.ChainStarted {
					data, ok := e.Data.(*statefeed.ChainStartedData)
					if !ok {
						log.Error("event data is not type *statefeed.ChainStartedData")
						return
					}
					// 接收到了pow chain启动的事件
					log.WithField("starttime", data.StartTime).Debug("Received chain start event")
					// 调用service的onPowchainStart
					s.onPowchainStart(s.ctx, data.StartTime)
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

// onPowchainStart initializes a series of deposits from the ChainStart deposits in the eth1
// deposit contract, initializes the beacon chain's state, and kicks off the beacon chain.
// onPowchainStart初始化一系列的deposits，从eth1的deposit contract的ChainStart deposits
// 初始化beacon chain的状态并且启动beacon chain
func (s *Service) onPowchainStart(ctx context.Context, genesisTime time.Time) {
	preGenesisState := s.cfg.ChainStartFetcher.PreGenesisState()
	// 初始化beacon chain，用preGenesisState以及eth1 data
	initializedState, err := s.initializeBeaconChain(ctx, genesisTime, preGenesisState, s.cfg.ChainStartFetcher.ChainStartEth1Data())
	if err != nil {
		log.Fatalf("Could not initialize beacon chain: %v", err)
	}
	// We start a counter to genesis, if needed.
	// 我们记录到genesis的计数，如果需要的话
	gRoot, err := initializedState.HashTreeRoot(s.ctx)
	if err != nil {
		log.Fatalf("Could not hash tree root genesis state: %v", err)
	}
	go slots.CountdownToGenesis(ctx, genesisTime, uint64(initializedState.NumValidators()), gRoot)

	// We send out a state initialized event to the rest of the services
	// running in the beacon node.
	// 我们发出一个state initialized 事件到剩余在beacon node中运行的services
	s.cfg.StateNotifier.StateFeed().Send(&feed.Event{
		// 发送初始化完成的事件
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
// 初始化state以及beacon chain的genesis block到持久化存储，基于从ChainStart事件中获取到的
// genesis timestamp
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
		// 不能初始化genesis state
		return nil, errors.Wrap(err, "could not initialize genesis state")
	}

	// 保存genesis state
	if err := s.saveGenesisData(ctx, genesisState); err != nil {
		return nil, errors.Wrap(err, "could not save genesis data")
	}

	// 初始化beacon chain的genesis state
	log.Info("Initialized beacon chain genesis state")

	// Clear out all pre-genesis data now that the state is initialized.
	// 清理所有的pre-genesis data，现在state已经初始化了
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
// 这在beacon chain第一次调用用来初始化保存gensis data到db的时候（state, block以及其他）被调用
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
	s.cfg.StateGen.SaveFinalizedState(0 /*slot*/, genesisBlkRoot, genesisState)

	if err := s.cfg.ForkChoiceStore.InsertNode(ctx, genesisState, genesisBlkRoot); err != nil {
		log.Fatalf("Could not process genesis block for fork choice: %v", err)
	}
	s.cfg.ForkChoiceStore.SetOriginRoot(genesisBlkRoot)
	// Set genesis as fully validated
	if err := s.cfg.ForkChoiceStore.SetOptimisticToValid(ctx, genesisBlkRoot); err != nil {
		return errors.Wrap(err, "Could not set optimistic status of genesis block to false")
	}
	s.cfg.ForkChoiceStore.SetGenesisTime(uint64(s.genesisTime.Unix()))

	s.setHead(genesisBlkRoot, genesisBlk, genesisState)
	return nil
}

// This returns true if block has been processed before. Two ways to verify the block has been processed:
// 1.) Check fork choice store.
// 2.) Check DB.
// Checking 1.) is ten times faster than checking 2.)
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
		log.Fatalf("Could not retrieve genesis state: %v", err)
	}
	gRoot, err := gState.HashTreeRoot(ctx)
	if err != nil {
		log.Fatalf("Could not hash tree root genesis state: %v", err)
	}
	go slots.CountdownToGenesis(ctx, genesisTime, uint64(gState.NumValidators()), gRoot)
}
