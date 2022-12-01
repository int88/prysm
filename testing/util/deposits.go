package util

import (
	"context"
	"sync"
	"testing"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/signing"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/config/params"
	"github.com/prysmaticlabs/prysm/container/trie"
	"github.com/prysmaticlabs/prysm/crypto/bls"
	"github.com/prysmaticlabs/prysm/crypto/hash"
	"github.com/prysmaticlabs/prysm/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/runtime/interop"
)

var lock sync.Mutex

// Caches
// 缓存的deposits
var cachedDeposits []*ethpb.Deposit
var privKeys []bls.SecretKey
var t *trie.SparseMerkleTrie

// DeterministicDepositsAndKeys returns the entered amount of deposits and secret keys.
// DeterministicDepositsAndKeys返回输入的deposits的数目以及secret keys
// The deposits are configured such that for deposit n the validator
// account is key n and the withdrawal account is key n+1.  As such,
// if all secret keys for n validators are required then numDeposits
// should be n+1.
// deposits被配置为，对于deposit n，validator是key n并且withdrawal account为key n+1
// 对于n validators所有secret keys都是需要的，之后numDeposits应该为n+1
func DeterministicDepositsAndKeys(numDeposits uint64) ([]*ethpb.Deposit, []bls.SecretKey, error) {
	resetCache()
	lock.Lock()
	defer lock.Unlock()
	var err error

	// Populate trie cache, if not initialized yet.
	// 填充trie cache，如果没有初始化完成的话
	if t == nil {
		t, err = trie.NewTrie(params.BeaconConfig().DepositContractTreeDepth)
		if err != nil {
			return nil, nil, errors.Wrap(err, "failed to create new trie")
		}
	}

	// If more deposits requested than cached, generate more.
	// 如果请求的deposits超过缓存的，产生更多
	if numDeposits > uint64(len(cachedDeposits)) {
		numExisting := uint64(len(cachedDeposits))
		numRequired := numDeposits - uint64(len(cachedDeposits))
		// Fetch the required number of keys.
		// 获取请求数目的keys
		secretKeys, publicKeys, err := interop.DeterministicallyGenerateKeys(numExisting, numRequired+1)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not create deterministic keys: ")
		}
		privKeys = append(privKeys, secretKeys[:len(secretKeys)-1]...)

		// Create the new deposits and add them to the trie.
		// 创建新的deposits并且将他们加入到trie
		for i := uint64(0); i < numRequired; i++ {
			balance := params.BeaconConfig().MaxEffectiveBalance
			deposit, err := signedDeposit(secretKeys[i], publicKeys[i].Marshal(), publicKeys[i+1].Marshal(), balance)
			if err != nil {
				return nil, nil, errors.Wrap(err, "could not create signed deposit")
			}
			cachedDeposits = append(cachedDeposits, deposit)

			hashedDeposit, err := deposit.Data.HashTreeRoot()
			if err != nil {
				return nil, nil, errors.Wrap(err, "could not tree hash deposit data")
			}

			if err = t.Insert(hashedDeposit[:], int(numExisting+i)); err != nil {
				return nil, nil, err
			}
		}
	}

	depositTrie, _, err := DeterministicDepositTrie(int(numDeposits)) // lint:ignore uintcast
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create deposit trie")
	}
	requestedDeposits := cachedDeposits[:numDeposits]
	for i := range requestedDeposits {
		proof, err := depositTrie.MerkleProof(i)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not create merkle proof")
		}
		requestedDeposits[i].Proof = proof
	}

	return requestedDeposits, privKeys[0:numDeposits], nil
}

// DepositsWithBalance generates N amount of deposits with the balances taken from the passed in balances array.
// DepositsWithBalance生成N个deposits，有着来自传入参数balances数组的balances
// If an empty array is passed,
func DepositsWithBalance(balances []uint64) ([]*ethpb.Deposit, *trie.SparseMerkleTrie, error) {
	var err error

	sparseTrie, err := trie.NewTrie(params.BeaconConfig().DepositContractTreeDepth)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create new trie")
	}

	numDeposits := uint64(len(balances))
	numExisting := uint64(len(cachedDeposits))
	numRequired := numDeposits - uint64(len(cachedDeposits))

	var secretKeys []bls.SecretKey
	var publicKeys []bls.PublicKey
	if numExisting >= numDeposits+1 {
		// 获取numDeposits个public key
		secretKeys = append(secretKeys, privKeys[:numDeposits+1]...)
		publicKeys = publicKeysFromSecrets(secretKeys)
	} else {
		secretKeys = append(secretKeys, privKeys[:numExisting]...)
		publicKeys = publicKeysFromSecrets(secretKeys)
		// Fetch enough keys for all deposits, since this function is uncached.
		// 获取足够的keys用于所有的deposits，因为这个函数是非缓存的
		newSecretKeys, newPublicKeys, err := interop.DeterministicallyGenerateKeys(numExisting, numRequired+1)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not create deterministic keys: ")
		}
		secretKeys = append(secretKeys, newSecretKeys...)
		publicKeys = append(publicKeys, newPublicKeys...)
	}

	deposits := make([]*ethpb.Deposit, numDeposits)
	// Create the new deposits and add them to the trie.
	// 创建新的deposits并且将它们加入到trie中
	for i := uint64(0); i < numDeposits; i++ {
		balance := params.BeaconConfig().MaxEffectiveBalance
		// lint:ignore uintcast -- test code
		if len(balances) == int(numDeposits) {
			balance = balances[i]
		}
		deposit, err := signedDeposit(secretKeys[i], publicKeys[i].Marshal(), publicKeys[i+1].Marshal(), balance)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not create signed deposit")
		}
		deposits[i] = deposit

		hashedDeposit, err := deposit.Data.HashTreeRoot()
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not tree hash deposit data")
		}

		// lint:ignore uintcast -- test code
		if err = sparseTrie.Insert(hashedDeposit[:], int(i)); err != nil {
			return nil, nil, err
		}
	}

	depositTrie, _, err := DepositTrieSubset(sparseTrie, int(numDeposits)) // lint:ignore uintcast -- test code
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create deposit trie")
	}
	for i := range deposits {
		proof, err := depositTrie.MerkleProof(i)
		if err != nil {
			// 不能创建 merkle proof
			return nil, nil, errors.Wrap(err, "could not create merkle proof")
		}
		// 设置proof
		deposits[i].Proof = proof
	}

	return deposits, sparseTrie, nil
}

func signedDeposit(
	secretKey bls.SecretKey,
	publicKey,
	withdrawalKey []byte,
	balance uint64,
) (*ethpb.Deposit, error) {
	withdrawalCreds := hash.Hash(withdrawalKey)
	withdrawalCreds[0] = params.BeaconConfig().BLSWithdrawalPrefixByte
	depositMessage := &ethpb.DepositMessage{
		PublicKey:             publicKey,
		Amount:                balance,
		WithdrawalCredentials: withdrawalCreds[:],
	}

	domain, err := signing.ComputeDomain(params.BeaconConfig().DomainDeposit, nil, nil)
	if err != nil {
		return nil, errors.Wrap(err, "could not compute domain")
	}
	root, err := depositMessage.HashTreeRoot()
	if err != nil {
		return nil, errors.Wrap(err, "could not get signing root of deposit data")
	}

	sigRoot, err := (&ethpb.SigningData{ObjectRoot: root[:], Domain: domain}).HashTreeRoot()
	if err != nil {
		return nil, err
	}
	depositData := &ethpb.Deposit_Data{
		PublicKey:             publicKey,
		Amount:                balance,
		WithdrawalCredentials: withdrawalCreds[:],
		Signature:             secretKey.Sign(sigRoot[:]).Marshal(),
	}

	// 构建deposit
	deposit := &ethpb.Deposit{
		Data: depositData,
	}
	return deposit, nil
}

// DeterministicDepositTrie returns a merkle trie of the requested size from the
// deterministic deposits.
// DeterministicDepositTrie返回请求的大小的merkle trie，从deterministic deposits
func DeterministicDepositTrie(size int) (*trie.SparseMerkleTrie, [][32]byte, error) {
	if t == nil {
		return nil, [][32]byte{}, errors.New("trie cache is empty, generate deposits at an earlier point")
	}

	return DepositTrieSubset(t, size)
}

// DepositTrieSubset takes in a full tree and the desired size and returns a subset of the deposit trie.
// DepositTrieSubset拿一个full tree和期望的size，返回deposit trie的一个子集
func DepositTrieSubset(sparseTrie *trie.SparseMerkleTrie, size int) (*trie.SparseMerkleTrie, [][32]byte, error) {
	if sparseTrie == nil {
		return nil, [][32]byte{}, errors.New("trie is empty")
	}

	items := sparseTrie.Items()
	if size > len(items) {
		return nil, [][32]byte{}, errors.New("requested a larger tree than amount of deposits")
	}

	items = items[:size]
	depositTrie, err := trie.GenerateTrieFromItems(items, params.BeaconConfig().DepositContractTreeDepth)
	if err != nil {
		return nil, [][32]byte{}, errors.Wrapf(err, "could not generate trie of %d length", size)
	}

	roots := make([][32]byte, len(items))
	for i, dep := range items {
		roots[i] = bytesutil.ToBytes32(dep)
	}
	return depositTrie, roots, nil
}

// DeterministicEth1Data takes an array of deposits and returns the eth1Data made from the deposit trie.
// DeterministicEth1Data获取一系列的deposits并且返回来自deposit trie的eth1Data
func DeterministicEth1Data(size int) (*ethpb.Eth1Data, error) {
	// 构建deposit trie
	depositTrie, _, err := DeterministicDepositTrie(size)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create trie")
	}
	// 获取deposit trie root
	root, err := depositTrie.HashTreeRoot()
	if err != nil {
		return nil, errors.Wrap(err, "failed to compute deposit trie root")
	}
	eth1Data := &ethpb.Eth1Data{
		BlockHash:   root[:],
		DepositRoot: root[:],
		//deposit的数目
		DepositCount: uint64(size),
	}
	return eth1Data, nil
}

// DeterministicGenesisState returns a genesis state made using the deterministic deposits.
// DeterministicGenesisState返回一个由deterministic deposits创建的genesis state
func DeterministicGenesisState(t testing.TB, numValidators uint64) (state.BeaconState, []bls.SecretKey) {
	// 创建numValidators数目的validators
	deposits, privKeys, err := DeterministicDepositsAndKeys(numValidators)
	if err != nil {
		t.Fatal(errors.Wrapf(err, "failed to get %d deposits", numValidators))
	}
	// 确定eth data
	eth1Data, err := DeterministicEth1Data(len(deposits))
	if err != nil {
		t.Fatal(errors.Wrapf(err, "failed to get eth1data for %d deposits", numValidators))
	}
	// 构建genesis state
	beaconState, err := transition.GenesisBeaconState(context.Background(), deposits, uint64(0), eth1Data)
	if err != nil {
		t.Fatal(errors.Wrapf(err, "failed to get genesis beacon state of %d validators", numValidators))
	}

	return beaconState, privKeys
}

// DepositTrieFromDeposits takes an array of deposits and returns the deposit trie.
func DepositTrieFromDeposits(deposits []*ethpb.Deposit) (*trie.SparseMerkleTrie, [][32]byte, error) {
	encodedDeposits := make([][]byte, len(deposits))
	for i := 0; i < len(encodedDeposits); i++ {
		hashedDeposit, err := deposits[i].Data.HashTreeRoot()
		if err != nil {
			return nil, [][32]byte{}, errors.Wrap(err, "could not tree hash deposit data")
		}
		encodedDeposits[i] = hashedDeposit[:]
	}

	depositTrie, err := trie.GenerateTrieFromItems(encodedDeposits, params.BeaconConfig().DepositContractTreeDepth)
	if err != nil {
		return nil, [][32]byte{}, errors.Wrap(err, "Could not generate deposit trie")
	}

	roots := make([][32]byte, len(deposits))
	for i, dep := range encodedDeposits {
		roots[i] = bytesutil.ToBytes32(dep)
	}

	return depositTrie, roots, nil
}

// resetCache clears out the old trie, private keys and deposits.
// resetCache清理old trie, private keys以及deposits
func resetCache() {
	lock.Lock()
	defer lock.Unlock()
	t = nil
	privKeys = []bls.SecretKey{}
	cachedDeposits = []*ethpb.Deposit{}
}

// DeterministicDepositsAndKeysSameValidator returns the entered amount of deposits and secret keys
// of the same validator. This is for negative test cases such as same deposits from same validators in a block don't
// result in duplicated validator indices.
// DeterministicDepositsAndKeysSameValidator返回同一个validator的输入的存款金额以及secret keys
// 这用于negative test cases，例如在一个block里面来自同一个validtor的同样的deposits，不会导致重复的validator indices
func DeterministicDepositsAndKeysSameValidator(numDeposits uint64) ([]*ethpb.Deposit, []bls.SecretKey, error) {
	resetCache()
	lock.Lock()
	defer lock.Unlock()
	var err error

	// Populate trie cache, if not initialized yet.
	// 填充trie cache，如果还没有初始化完成的话
	if t == nil {
		t, err = trie.NewTrie(params.BeaconConfig().DepositContractTreeDepth)
		if err != nil {
			return nil, nil, errors.Wrap(err, "failed to create new trie")
		}
	}

	// If more deposits requested than cached, generate more.
	// 如果请求的deposits超过缓存的，则生成更多
	if numDeposits > uint64(len(cachedDeposits)) {
		numExisting := uint64(len(cachedDeposits))
		numRequired := numDeposits - uint64(len(cachedDeposits))
		// Fetch the required number of keys.
		// 获取需要的keys的数目
		secretKeys, publicKeys, err := interop.DeterministicallyGenerateKeys(numExisting, numRequired+1)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not create deterministic keys: ")
		}
		privKeys = append(privKeys, secretKeys[:len(secretKeys)-1]...)

		// Create the new deposits and add them to the trie. Always use the first validator to create deposit
		// 创建新的deposits并且将它们添加到trie，总是使用第一个validator来创建deposit
		for i := uint64(0); i < numRequired; i++ {
			withdrawalCreds := hash.Hash(publicKeys[1].Marshal())
			withdrawalCreds[0] = params.BeaconConfig().BLSWithdrawalPrefixByte

			// 构建deposit message
			depositMessage := &ethpb.DepositMessage{
				PublicKey:             publicKeys[1].Marshal(),
				Amount:                params.BeaconConfig().MaxEffectiveBalance,
				WithdrawalCredentials: withdrawalCreds[:],
			}

			domain, err := signing.ComputeDomain(params.BeaconConfig().DomainDeposit, nil, nil)
			if err != nil {
				return nil, nil, errors.Wrap(err, "could not compute domain")
			}
			root, err := depositMessage.HashTreeRoot()
			if err != nil {
				return nil, nil, errors.Wrap(err, "could not get signing root of deposit data")
			}
			sigRoot, err := (&ethpb.SigningData{ObjectRoot: root[:], Domain: domain}).HashTreeRoot()
			if err != nil {
				// 不能获取deposit data以及domain的signing root
				return nil, nil, errors.Wrap(err, "could not get signing root of deposit data and domain")
			}
			// Always use the same validator to sign
			// 总是使用同一个validator去签名
			depositData := &ethpb.Deposit_Data{
				PublicKey:             depositMessage.PublicKey,
				Amount:                depositMessage.Amount,
				WithdrawalCredentials: depositMessage.WithdrawalCredentials,
				Signature:             secretKeys[1].Sign(sigRoot[:]).Marshal(),
			}
			// 构建deposit
			deposit := &ethpb.Deposit{
				Data: depositData,
			}
			cachedDeposits = append(cachedDeposits, deposit)

			hashedDeposit, err := deposit.Data.HashTreeRoot()
			if err != nil {
				return nil, nil, errors.Wrap(err, "could not tree hash deposit data")
			}

			// 在tree中插入hashed deposit
			if err = t.Insert(hashedDeposit[:], int(numExisting+i)); err != nil {
				return nil, nil, err
			}
		}
	}

	// lint:ignore uintcast -- test code
	depositTrie, _, err := DeterministicDepositTrie(int(numDeposits))
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create deposit trie")
	}
	requestedDeposits := cachedDeposits[:numDeposits]
	for i := range requestedDeposits {
		proof, err := depositTrie.MerkleProof(i)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not create merkle proof")
		}
		requestedDeposits[i].Proof = proof
	}

	return requestedDeposits, privKeys[0:numDeposits], nil
}

func publicKeysFromSecrets(secretKeys []bls.SecretKey) []bls.PublicKey {
	publicKeys := make([]bls.PublicKey, len(secretKeys))
	for i, secretKey := range secretKeys {
		publicKeys[i] = secretKey.PublicKey()
	}
	return publicKeys
}
