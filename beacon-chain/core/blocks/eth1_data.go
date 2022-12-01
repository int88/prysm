package blocks

import (
	"bytes"
	"context"
	"errors"

	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/config/params"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
)

// ProcessEth1DataInBlock is an operation performed on each
// beacon block to ensure the ETH1 data votes are processed
// into the beacon state.
// ProcessEth1DataInBlock是在每个beacon block上执行的操作，来确保ETH1 data votes
// 在beacon state上处理
//
// Official spec definition:
//   def process_eth1_data(state: BeaconState, body: BeaconBlockBody) -> None:
//    state.eth1_data_votes.append(body.eth1_data)
//    if state.eth1_data_votes.count(body.eth1_data) * 2 > EPOCHS_PER_ETH1_VOTING_PERIOD * SLOTS_PER_EPOCH:
//        state.eth1_data = body.eth1_data
func ProcessEth1DataInBlock(_ context.Context, beaconState state.BeaconState, eth1Data *ethpb.Eth1Data) (state.BeaconState, error) {
	if beaconState == nil || beaconState.IsNil() {
		return nil, errors.New("nil state")
	}
	// 扩展eth1 data votes
	if err := beaconState.AppendEth1DataVotes(eth1Data); err != nil {
		return nil, err
	}
	hasSupport, err := Eth1DataHasEnoughSupport(beaconState, eth1Data)
	if err != nil {
		return nil, err
	}
	if hasSupport {
		if err := beaconState.SetEth1Data(eth1Data); err != nil {
			return nil, err
		}
	}
	return beaconState, nil
}

// AreEth1DataEqual checks equality between two eth1 data objects.
// AreEth1DataEqual检查两个eth1 data对象是否相等
func AreEth1DataEqual(a, b *ethpb.Eth1Data) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.DepositCount == b.DepositCount &&
		bytes.Equal(a.BlockHash, b.BlockHash) &&
		bytes.Equal(a.DepositRoot, b.DepositRoot)
}

// Eth1DataHasEnoughSupport returns true when the given eth1data has more than 50% votes in the
// eth1 voting period. A vote is cast by including eth1data in a block and part of state processing
// appends eth1data to the state in the Eth1DataVotes list. Iterating through this list checks the
// votes to see if they match the eth1data.
// Eth1DataHasEnoughSupport返回true，当给定的eth1data有超过50%的votes，在eth1的voting period
// 一个vote可以被转换，当包括eth1data，在一个block，并且部分state processing将eth1data扩展到Eth1DataVotes
// list的state中，遍历list查看votes，它们是否匹配eth1data
func Eth1DataHasEnoughSupport(beaconState state.ReadOnlyBeaconState, data *ethpb.Eth1Data) (bool, error) {
	voteCount := uint64(0)
	data = ethpb.CopyETH1Data(data)

	for _, vote := range beaconState.Eth1DataVotes() {
		if AreEth1DataEqual(vote, data) {
			voteCount++
		}
	}

	// If 50+% majority converged on the same eth1data, then it has enough support to update the
	// state.
	// 如果50+%的大多数覆盖了同样的eth1data，那么它有足够的支持来更新state
	support := params.BeaconConfig().SlotsPerEpoch.Mul(uint64(params.BeaconConfig().EpochsPerEth1VotingPeriod))
	return voteCount*2 > uint64(support), nil
}
