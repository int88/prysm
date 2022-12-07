package execution

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed"
	statefeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/state"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	coreState "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/execution/types"
	statenative "github.com/prysmaticlabs/prysm/v3/beacon-chain/state/state-native"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	contracts "github.com/prysmaticlabs/prysm/v3/contracts/deposit"
	"github.com/prysmaticlabs/prysm/v3/crypto/hash"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
	"github.com/sirupsen/logrus"
)

var (
	depositEventSignature = hash.HashKeccak256([]byte("DepositEvent(bytes,bytes,bytes,bytes,bytes)"))
)

const eth1DataSavingInterval = 1000
const maxTolerableDifference = 50
const defaultEth1HeaderReqLimit = uint64(1000)
const depositLogRequestLimit = 10000
const additiveFactorMultiplier = 0.10
const multiplicativeDecreaseDivisor = 2

var errTimedOut = errors.New("net/http: request canceled")

func tooMuchDataRequestedError(err error) bool {
	// this error is only infura specific (other providers might have different error messages)
	return err.Error() == "query returned more than 10000 results"
}

func clientTimedOutError(err error) bool {
	return strings.Contains(err.Error(), errTimedOut.Error())
}

// GenesisExecutionChainInfo retrieves the genesis time and execution block number of the beacon chain
// from the deposit contract.
func (s *Service) GenesisExecutionChainInfo() (uint64, *big.Int) {
	return s.chainStartData.GenesisTime, big.NewInt(int64(s.chainStartData.GenesisBlock))
}

// ProcessETH1Block processes logs from the provided eth1 block.
// ProcessETH1Block处理提供的eth1 block的logs
func (s *Service) ProcessETH1Block(ctx context.Context, blkNum *big.Int) error {
	query := ethereum.FilterQuery{
		Addresses: []common.Address{
			s.cfg.depositContractAddr,
		},
		FromBlock: blkNum,
		ToBlock:   blkNum,
	}
	logs, err := s.httpLogger.FilterLogs(ctx, query)
	if err != nil {
		return err
	}
	for _, filterLog := range logs {
		// ignore logs that are not of the required block number
		// 如果logs不属于请求的block number，则忽略
		if filterLog.BlockNumber != blkNum.Uint64() {
			continue
		}
		if err := s.ProcessLog(ctx, filterLog); err != nil {
			return errors.Wrap(err, "could not process log")
		}
	}
	if !s.chainStartData.Chainstarted {
		// 如果chain还没有启动，则还需要处理ChainStart
		if err := s.processChainStartFromBlockNum(ctx, blkNum); err != nil {
			return err
		}
	}
	return nil
}

// ProcessLog is the main method which handles the processing of all
// logs from the deposit contract on the eth1 chain.
// ProcessLog是主要的方法用于处理所有来自deposit contract的logs
func (s *Service) ProcessLog(ctx context.Context, depositLog gethtypes.Log) error {
	s.processingLock.RLock()
	defer s.processingLock.RUnlock()
	// Process logs according to their event signature.
	// 根据event signature对logs进行处理
	if depositLog.Topics[0] == depositEventSignature {
		if err := s.ProcessDepositLog(ctx, depositLog); err != nil {
			return errors.Wrap(err, "Could not process deposit log")
		}
		if s.lastReceivedMerkleIndex%eth1DataSavingInterval == 0 {
			return s.savePowchainData(ctx)
		}
		return nil
	}
	log.WithField("signature", fmt.Sprintf("%#x", depositLog.Topics[0])).Debug("Not a valid event signature")
	return nil
}

// ProcessDepositLog processes the log which had been received from
// the eth1 chain by trying to ascertain which participant deposited
// in the contract.
// ProcessDepositLog处理从eth1中接收到的log，通过试着确定合约的参与者
func (s *Service) ProcessDepositLog(ctx context.Context, depositLog gethtypes.Log) error {
	// 对deposit log进行解码，获取pubkey, withdrawal credentials，amount, signature，以及merkleTreeIndex
	pubkey, withdrawalCredentials, amount, signature, merkleTreeIndex, err := contracts.UnpackDepositLogData(depositLog.Data)
	if err != nil {
		return errors.Wrap(err, "Could not unpack log")
	}
	// If we have already seen this Merkle index, skip processing the log.
	// This can happen sometimes when we receive the same log twice from the
	// ETH1.0 network, and prevents us from updating our trie
	// with the same log twice, causing an inconsistent state root.
	// 如果我们已经见过这个Merkle index，跳过处理这个log，这有时候会发生，当我们从ETH1.0 network
	// 收到同一个log两次，防止用同样的log更新我们的trie两次，导致不一致的state root
	index := int64(binary.LittleEndian.Uint64(merkleTreeIndex)) // lint:ignore uintcast -- MerkleTreeIndex should not exceed int64 in your lifetime.
	if index <= s.lastReceivedMerkleIndex {
		return nil
	}

	if index != s.lastReceivedMerkleIndex+1 {
		missedDepositLogsCount.Inc()
		return errors.Errorf("received incorrect merkle index: wanted %d but got %d", s.lastReceivedMerkleIndex+1, index)
	}
	s.lastReceivedMerkleIndex = index

	// We then decode the deposit input in order to create a deposit object
	// we can store in our persistent DB.
	// 创建一个deposit对象，我们可以存储在我们的DB中
	depositData := &ethpb.Deposit_Data{
		Amount:                bytesutil.FromBytes8(amount),
		PublicKey:             pubkey,
		Signature:             signature,
		WithdrawalCredentials: withdrawalCredentials,
	}

	depositHash, err := depositData.HashTreeRoot()
	if err != nil {
		return errors.Wrap(err, "unable to determine hashed value of deposit")
	}

	// Defensive check to validate incoming index.
	// 防御性检查，对于incoming index
	if s.depositTrie.NumOfItems() != int(index) {
		return errors.Errorf("invalid deposit index received: wanted %d but got %d", s.depositTrie.NumOfItems(), index)
	}
	// 插入deposit trie
	if err = s.depositTrie.Insert(depositHash[:], int(index)); err != nil {
		return err
	}

	deposit := &ethpb.Deposit{
		Data: depositData,
	}
	// Only generate the proofs during pre-genesis.
	// 只有在pre-genesis的时候生成proofs
	if !s.chainStartData.Chainstarted {
		proof, err := s.depositTrie.MerkleProof(int(index))
		if err != nil {
			return errors.Wrap(err, "unable to generate merkle proof for deposit")
		}
		deposit.Proof = proof
	}

	// We always store all historical deposits in the DB.
	// 我们总是存储所有的historical deposits到DB中
	root, err := s.depositTrie.HashTreeRoot()
	if err != nil {
		return errors.Wrap(err, "unable to determine root of deposit trie")
	}
	// 在depositCache中插入deposit
	err = s.cfg.depositCache.InsertDeposit(ctx, deposit, depositLog.BlockNumber, index, root)
	if err != nil {
		return errors.Wrap(err, "unable to insert deposit into cache")
	}
	validData := true
	if !s.chainStartData.Chainstarted {
		s.chainStartData.ChainstartDeposits = append(s.chainStartData.ChainstartDeposits, deposit)
		root, err := s.depositTrie.HashTreeRoot()
		if err != nil {
			return errors.Wrap(err, "unable to determine root of deposit trie")
		}
		eth1Data := &ethpb.Eth1Data{
			DepositRoot:  root[:],
			DepositCount: uint64(len(s.chainStartData.ChainstartDeposits)),
		}
		if err := s.processDeposit(ctx, eth1Data, deposit); err != nil {
			log.WithError(err).Error("Invalid deposit processed")
			validData = false
		}
	} else {
		root, err := s.depositTrie.HashTreeRoot()
		if err != nil {
			return errors.Wrap(err, "unable to determine root of deposit trie")
		}
		s.cfg.depositCache.InsertPendingDeposit(ctx, deposit, depositLog.BlockNumber, index, root)
	}
	if validData {
		log.WithFields(logrus.Fields{
			"eth1Block":       depositLog.BlockNumber,
			"publicKey":       fmt.Sprintf("%#x", depositData.PublicKey),
			"merkleTreeIndex": index,
		}).Debug("Deposit registered from deposit contract")
		validDepositsCount.Inc()
		// Notify users what is going on, from time to time.
		// 不时地通知用户发生了什么
		if !s.chainStartData.Chainstarted {
			deposits := len(s.chainStartData.ChainstartDeposits)
			if deposits%512 == 0 {
				valCount, err := helpers.ActiveValidatorCount(ctx, s.preGenesisState, 0)
				if err != nil {
					log.WithError(err).Error("Could not determine active validator count from pre genesis state")
				}
				log.WithFields(logrus.Fields{
					"deposits":          deposits,
					"genesisValidators": valCount,
				}).Info("Processing deposits from Ethereum 1 chain")
			}
		}
	} else {
		log.WithFields(logrus.Fields{
			"eth1Block":       depositLog.BlockHash.Hex(),
			"eth1Tx":          depositLog.TxHash.Hex(),
			"merkleTreeIndex": index,
		}).Info("Invalid deposit registered in deposit contract")
	}
	return nil
}

// ProcessChainStart processes the log which had been received from
// the eth1 chain by trying to determine when to start the beacon chain.
// ProcessChainStart处理获取自eth1 chain的log，通过试着决定什么时候开始beacon chain
func (s *Service) ProcessChainStart(genesisTime uint64, eth1BlockHash [32]byte, blockNumber *big.Int) {
	s.chainStartData.Chainstarted = true
	s.chainStartData.GenesisBlock = blockNumber.Uint64()

	chainStartTime := time.Unix(int64(genesisTime), 0) // lint:ignore uintcast -- Genesis time wont exceed int64 in your lifetime.

	for i := range s.chainStartData.ChainstartDeposits {
		// 获取deposit的proof
		proof, err := s.depositTrie.MerkleProof(i)
		if err != nil {
			log.WithError(err).Error("unable to generate deposit proof")
		}
		s.chainStartData.ChainstartDeposits[i].Proof = proof
	}

	root, err := s.depositTrie.HashTreeRoot()
	if err != nil { // This should never happen.
		log.WithError(err).Error("unable to determine root of deposit trie, aborting chain start")
		return
	}
	s.chainStartData.Eth1Data = &ethpb.Eth1Data{
		// 保存eth1 data
		DepositCount: uint64(len(s.chainStartData.ChainstartDeposits)),
		DepositRoot:  root[:],
		BlockHash:    eth1BlockHash[:],
	}

	log.WithFields(logrus.Fields{
		"ChainStartTime": chainStartTime,
	}).Info("Minimum number of validators reached for beacon-chain to start")
	// 达到了最少数目的validators来启动beacon-chain
	s.cfg.stateNotifier.StateFeed().Send(&feed.Event{
		// 发送ChainStarted事件
		Type: statefeed.ChainStarted,
		Data: &statefeed.ChainStartedData{
			StartTime: chainStartTime,
		},
	})
	// 保存powchain data
	if err := s.savePowchainData(s.ctx); err != nil {
		// continue on if the save fails as this will get re-saved
		// in the next interval.
		// 继续，如果保存失败的话，这会在下一个interval导致re-saved
		log.Error(err)
	}
}

// createGenesisTime adds in the genesis delay to the eth1 block time
// on which it was triggered.
func createGenesisTime(timeStamp uint64) uint64 {
	return timeStamp + params.BeaconConfig().GenesisDelay
}

// processPastLogs processes all the past logs from the deposit contract and
// updates the deposit trie with the data from each individual log.
// processPastLogs处理来自deposit contract的所有past logs并且更新deposit trie，用来自
// 单个log的data
func (s *Service) processPastLogs(ctx context.Context) error {
	currentBlockNum := s.latestEth1Data.LastRequestedBlock
	deploymentBlock := params.BeaconNetworkConfig().ContractDeploymentBlock
	// Start from the deployment block if our last requested block
	// is behind it. This is as the deposit logs can only start from the
	// block of the deployment of the deposit contract.
	// 从deployment block开始，如果我们最后请求的block在它之后，因为deposit logs
	// 只能从部署deposit contract的deployment之后开始
	if deploymentBlock > currentBlockNum {
		currentBlockNum = deploymentBlock
	}
	// To store all blocks.
	// 存储所有的blocks
	headersMap := make(map[uint64]*types.HeaderInfo)
	rawLogCount, err := s.depositContractCaller.GetDepositCount(&bind.CallOpts{})
	if err != nil {
		return err
	}
	logCount := binary.LittleEndian.Uint64(rawLogCount)

	latestFollowHeight, err := s.followedBlockHeight(ctx)
	if err != nil {
		return err
	}

	batchSize := s.cfg.eth1HeaderReqLimit
	additiveFactor := uint64(float64(batchSize) * additiveFactorMultiplier)

	for currentBlockNum < latestFollowHeight {
		// 批量处理block
		currentBlockNum, batchSize, err = s.processBlockInBatch(ctx, currentBlockNum, latestFollowHeight, batchSize, additiveFactor, logCount, headersMap)
		if err != nil {
			return err
		}
	}

	s.latestEth1DataLock.Lock()
	// 设置当前的block number
	s.latestEth1Data.LastRequestedBlock = currentBlockNum
	s.latestEth1DataLock.Unlock()

	c, err := s.cfg.beaconDB.FinalizedCheckpoint(ctx)
	if err != nil {
		return err
	}
	fRoot := bytesutil.ToBytes32(c.Root)
	// Return if no checkpoint exists yet.
	// 返回，如果没有checkpoint存在
	if fRoot == params.BeaconConfig().ZeroHash {
		return nil
	}
	fState := s.cfg.finalizedStateAtStartup
	isNil := fState == nil || fState.IsNil()

	// If processing past logs take a long time, we
	// need to check if this is the correct finalized
	// state we are referring to and whether our cached
	// finalized state is referring to our current finalized checkpoint.
	// The current code does ignore an edge case where the finalized
	// block is in a different epoch from the checkpoint's epoch.
	// This only happens in skipped slots, so pruning it is not an issue.
	// 如果处理past logs花费很长时间，我们需要检查是否这是正确的finalized state
	// 以及我们缓存的finalized state引用我们当前的finalized checkpoint
	// 当前的代码确实忽略了一个边界条件，当finalized block在不同的epoch，对于checkpoint的epoch
	// 这只会在skipped slots的时候发生，因此清理它不是一个问题
	if isNil || slots.ToEpoch(fState.Slot()) != c.Epoch {
		fState, err = s.cfg.stateGen.StateByRoot(ctx, fRoot)
		if err != nil {
			return err
		}
	}
	if fState != nil && !fState.IsNil() && fState.Eth1DepositIndex() > 0 {
		// 移除pending deposits?
		s.cfg.depositCache.PrunePendingDeposits(ctx, int64(fState.Eth1DepositIndex())) // lint:ignore uintcast -- Deposit index should not exceed int64 in your lifetime.
	}
	return nil
}

func (s *Service) processBlockInBatch(ctx context.Context, currentBlockNum uint64, latestFollowHeight uint64, batchSize uint64, additiveFactor uint64, logCount uint64, headersMap map[uint64]*types.HeaderInfo) (uint64, uint64, error) {
	// Batch request the desired headers and store them in a
	// map for quick access.
	requestHeaders := func(startBlk uint64, endBlk uint64) error {
		headers, err := s.batchRequestHeaders(startBlk, endBlk)
		if err != nil {
			return err
		}
		for _, h := range headers {
			if h != nil && h.Number != nil {
				headersMap[h.Number.Uint64()] = h
			}
		}
		return nil
	}

	start := currentBlockNum
	end := currentBlockNum + batchSize
	// Appropriately bound the request, as we do not
	// want request blocks beyond the current follow distance.
	if end > latestFollowHeight {
		end = latestFollowHeight
	}
	query := ethereum.FilterQuery{
		Addresses: []common.Address{
			s.cfg.depositContractAddr,
		},
		FromBlock: big.NewInt(0).SetUint64(start),
		ToBlock:   big.NewInt(0).SetUint64(end),
	}
	remainingLogs := logCount - uint64(s.lastReceivedMerkleIndex+1)
	// only change the end block if the remaining logs are below the required log limit.
	// reset our query and end block in this case.
	withinLimit := remainingLogs < depositLogRequestLimit
	aboveFollowHeight := end >= latestFollowHeight
	if withinLimit && aboveFollowHeight {
		query.ToBlock = big.NewInt(0).SetUint64(latestFollowHeight)
		end = latestFollowHeight
	}
	logs, err := s.httpLogger.FilterLogs(ctx, query)
	if err != nil {
		if tooMuchDataRequestedError(err) {
			if batchSize == 0 {
				return 0, 0, errors.New("batch size is zero")
			}

			// multiplicative decrease
			batchSize /= multiplicativeDecreaseDivisor
			return currentBlockNum, batchSize, nil
		}
		return 0, 0, err
	}
	// Only request headers before chainstart to correctly determine
	// genesis.
	if !s.chainStartData.Chainstarted {
		if err := requestHeaders(start, end); err != nil {
			return 0, 0, err
		}
	}

	s.latestEth1DataLock.RLock()
	lastReqBlock := s.latestEth1Data.LastRequestedBlock
	s.latestEth1DataLock.RUnlock()

	for _, filterLog := range logs {
		if filterLog.BlockNumber > currentBlockNum {
			if err := s.checkHeaderRange(ctx, currentBlockNum, filterLog.BlockNumber-1, headersMap, requestHeaders); err != nil {
				return 0, 0, err
			}
			// set new block number after checking for chainstart for previous block.
			s.latestEth1DataLock.Lock()
			s.latestEth1Data.LastRequestedBlock = currentBlockNum
			s.latestEth1DataLock.Unlock()
			currentBlockNum = filterLog.BlockNumber
		}
		if err := s.ProcessLog(ctx, filterLog); err != nil {
			// In the event the execution client gives us a garbled/bad log
			// we reset the last requested block to the previous valid block range. This
			// prevents the beacon from advancing processing of logs to another range
			// in the event of an execution client failure.
			s.latestEth1DataLock.Lock()
			s.latestEth1Data.LastRequestedBlock = lastReqBlock
			s.latestEth1DataLock.Unlock()
			return 0, 0, err
		}
	}
	if err := s.checkHeaderRange(ctx, currentBlockNum, end, headersMap, requestHeaders); err != nil {
		return 0, 0, err
	}
	currentBlockNum = end

	if batchSize < s.cfg.eth1HeaderReqLimit {
		// update the batchSize with additive increase
		batchSize += additiveFactor
		if batchSize > s.cfg.eth1HeaderReqLimit {
			batchSize = s.cfg.eth1HeaderReqLimit
		}
	}
	return currentBlockNum, batchSize, nil
}

// requestBatchedHeadersAndLogs requests and processes all the headers and
// logs from the period last polled to now.
// requestBatchedHeadersAndLogs请求以及处理所有的headers以及logs，从上次拉取的到现在
func (s *Service) requestBatchedHeadersAndLogs(ctx context.Context) error {
	// We request for the nth block behind the current head, in order to have
	// stabilized logs when we retrieve it from the eth1 chain.
	// 我们请求当前head之前的第n个block，为了有稳定的blocks，当我们从eth1 chain获取的时候

	requestedBlock, err := s.followedBlockHeight(ctx)
	if err != nil {
		return err
	}
	if requestedBlock > s.latestEth1Data.LastRequestedBlock &&
		requestedBlock-s.latestEth1Data.LastRequestedBlock > maxTolerableDifference {
		log.Infof("Falling back to historical headers and logs sync. Current difference is %d", requestedBlock-s.latestEth1Data.LastRequestedBlock)
		return s.processPastLogs(ctx)
	}
	for i := s.latestEth1Data.LastRequestedBlock + 1; i <= requestedBlock; i++ {
		// Cache eth1 block header here.
		// 缓存eth1 block header
		_, err := s.BlockHashByHeight(ctx, big.NewInt(0).SetUint64(i))
		if err != nil {
			return err
		}
		// 处理eth1 block，获取deposit log，加入deposit等等
		err = s.ProcessETH1Block(ctx, big.NewInt(0).SetUint64(i))
		if err != nil {
			return err
		}
		s.latestEth1DataLock.Lock()
		s.latestEth1Data.LastRequestedBlock = i
		s.latestEth1DataLock.Unlock()
	}

	return nil
}

func (s *Service) retrieveBlockHashAndTime(ctx context.Context, blkNum *big.Int) ([32]byte, uint64, error) {
	bHash, err := s.BlockHashByHeight(ctx, blkNum)
	if err != nil {
		return [32]byte{}, 0, errors.Wrap(err, "could not get eth1 block hash")
	}
	if bHash == [32]byte{} {
		return [32]byte{}, 0, errors.Wrap(err, "got empty block hash")
	}
	timeStamp, err := s.BlockTimeByHeight(ctx, blkNum)
	if err != nil {
		return [32]byte{}, 0, errors.Wrap(err, "could not get block timestamp")
	}
	return bHash, timeStamp, nil
}

func (s *Service) processChainStartFromBlockNum(ctx context.Context, blkNum *big.Int) error {
	bHash, timeStamp, err := s.retrieveBlockHashAndTime(ctx, blkNum)
	if err != nil {
		return err
	}
	s.processChainStartIfReady(ctx, bHash, blkNum, timeStamp)
	return nil
}

func (s *Service) processChainStartFromHeader(ctx context.Context, header *types.HeaderInfo) {
	s.processChainStartIfReady(ctx, header.Hash, header.Number, header.Time)
}

func (s *Service) checkHeaderRange(ctx context.Context, start, end uint64, headersMap map[uint64]*types.HeaderInfo,
	requestHeaders func(uint64, uint64) error) error {
	for i := start; i <= end; i++ {
		if !s.chainStartData.Chainstarted {
			h, ok := headersMap[i]
			if !ok {
				if err := requestHeaders(i, end); err != nil {
					return err
				}
				// Retry this block.
				i--
				continue
			}
			s.processChainStartFromHeader(ctx, h)
		}
	}
	return nil
}

// retrieves the current active validator count and genesis time from
// the provided block time.
// 获取当前的active validator count以及genesis time，从提供的block time
func (s *Service) currentCountAndTime(ctx context.Context, blockTime uint64) (uint64, uint64) {
	if s.preGenesisState.NumValidators() == 0 {
		return 0, 0
	}
	valCount, err := helpers.ActiveValidatorCount(ctx, s.preGenesisState, 0)
	if err != nil {
		// 从pre genesis state不能决定active validator
		log.WithError(err).Error("Could not determine active validator count from pre genesis state")
		return 0, 0
	}
	return valCount, createGenesisTime(blockTime)
}

func (s *Service) processChainStartIfReady(ctx context.Context, blockHash [32]byte, blockNumber *big.Int, blockTime uint64) {
	valCount, genesisTime := s.currentCountAndTime(ctx, blockTime)
	if valCount == 0 {
		return
	}
	triggered := coreState.IsValidGenesisState(valCount, genesisTime)
	if triggered {
		s.chainStartData.GenesisTime = genesisTime
		s.ProcessChainStart(s.chainStartData.GenesisTime, blockHash, blockNumber)
	}
}

// savePowchainData saves all powchain related metadata to disk.
// savePowchainData保存所有powchain相关的元数据到磁盘
func (s *Service) savePowchainData(ctx context.Context) error {
	pbState, err := statenative.ProtobufBeaconStatePhase0(s.preGenesisState.ToProtoUnsafe())
	if err != nil {
		return err
	}
	eth1Data := &ethpb.ETH1ChainData{
		CurrentEth1Data: s.latestEth1Data,
		ChainstartData:  s.chainStartData,
		BeaconState:     pbState, // I promise not to mutate it!
		// 获取deposit trie
		Trie: s.depositTrie.ToProto(),
		// 获取所有的deposti containers
		DepositContainers: s.cfg.depositCache.AllDepositContainers(ctx),
	}
	return s.cfg.beaconDB.SaveExecutionChainData(ctx, eth1Data)
}
