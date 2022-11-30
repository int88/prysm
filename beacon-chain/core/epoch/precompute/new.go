// Package precompute provides gathering of nicely-structured
// data important to feed into epoch processing, such as attesting
// records and balances, for faster computation.
// precompute包提供了对于结构良好的数据的收集，对于epoch processing的处理
// 很重要，例如attesting records以及balances，为了更快的计算
package precompute

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/config/params"
	"go.opencensus.io/trace"
)

// New gets called at the beginning of process epoch cycle to return
// pre computed instances of validators attesting records and total
// balances attested in an epoch.
// New在处理每个epoch cycle的开始被调用，来返回提前计算的validators attesting records的实例
// 以及total balances attested，在一个epoch中
func New(ctx context.Context, s state.BeaconState) ([]*Validator, *Balance, error) {
	_, span := trace.StartSpan(ctx, "precomputeEpoch.New")
	defer span.End()

	pValidators := make([]*Validator, s.NumValidators())
	pBal := &Balance{}

	currentEpoch := time.CurrentEpoch(s)
	prevEpoch := time.PrevEpoch(s)

	if err := s.ReadFromEveryValidator(func(idx int, val state.ReadOnlyValidator) error {
		// Was validator withdrawable or slashed
		withdrawable := prevEpoch+1 >= val.WithdrawableEpoch()
		pVal := &Validator{
			IsSlashed:                    val.Slashed(),
			IsWithdrawableCurrentEpoch:   withdrawable,
			CurrentEpochEffectiveBalance: val.EffectiveBalance(),
		}
		// Was validator active current epoch
		if helpers.IsActiveValidatorUsingTrie(val, currentEpoch) {
			pVal.IsActiveCurrentEpoch = true
			pBal.ActiveCurrentEpoch += val.EffectiveBalance()
		}
		// Was validator active previous epoch
		if helpers.IsActiveValidatorUsingTrie(val, prevEpoch) {
			pVal.IsActivePrevEpoch = true
			pBal.ActivePrevEpoch += val.EffectiveBalance()
		}
		// Set inclusion slot and inclusion distance to be max, they will be compared and replaced
		// with the lower values
		pVal.InclusionSlot = params.BeaconConfig().FarFutureSlot
		pVal.InclusionDistance = params.BeaconConfig().FarFutureSlot

		pValidators[idx] = pVal
		return nil
	}); err != nil {
		return nil, nil, errors.Wrap(err, "failed to initialize precompute")
	}
	return pValidators, pBal, nil
}
