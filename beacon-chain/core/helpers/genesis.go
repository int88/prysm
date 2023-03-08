package helpers

import (
	"fmt"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/container/trie"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
)

// UpdateGenesisEth1Data updates eth1 data for genesis state.
// UpdateGenesisEth1Data为genesis state更新eth1 data
func UpdateGenesisEth1Data(state state.BeaconState, deposits []*ethpb.Deposit, eth1Data *ethpb.Eth1Data) (state.BeaconState, error) {
	if eth1Data == nil {
		return nil, errors.New("no eth1data provided for genesis state")
	}

	leaves := make([][]byte, 0, len(deposits))
	for _, deposit := range deposits {
		// 遍历deposits
		if deposit == nil || deposit.Data == nil {
			return nil, fmt.Errorf("nil deposit or deposit with nil data cannot be processed: %v", deposit)
		}
		hash, err := deposit.Data.HashTreeRoot()
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, hash[:])
	}
	var t *trie.SparseMerkleTrie
	var err error
	// 构建trie树
	if len(leaves) > 0 {
		t, err = trie.GenerateTrieFromItems(leaves, params.BeaconConfig().DepositContractTreeDepth)
		if err != nil {
			return nil, err
		}
	} else {
		t, err = trie.NewTrie(params.BeaconConfig().DepositContractTreeDepth)
		if err != nil {
			return nil, err
		}
	}

	depositRoot, err := t.HashTreeRoot()
	if err != nil {
		return nil, err
	}
	eth1Data.DepositRoot = depositRoot[:]
	// 在state中设置Eth1Data
	err = state.SetEth1Data(eth1Data)
	if err != nil {
		return nil, err
	}
	return state, nil
}
