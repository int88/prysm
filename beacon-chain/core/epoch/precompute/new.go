// Package precompute provides gathering of nicely-structured
// data important to feed into epoch processing, such as attesting
// records and balances, for faster computation.
// precompute包提供了收集nicely-structured data，对于epoch processing非常重要
// 例如attesting recrods以及balances，为了更快的计算
package precompute

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/time"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"go.opencensus.io/trace"
)

// New gets called at the beginning of process epoch cycle to return
// pre computed instances of validators attesting records and total
// balances attested in an epoch.
// New在每个epoch周期的开始被调用，来返回提前计算的validators attesting records的实例
// 以及在一个epoch中全部的attested balance
func New(ctx context.Context, s state.BeaconState) ([]*Validator, *Balance, error) {
	ctx, span := trace.StartSpan(ctx, "precomputeEpoch.New")
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
		// validator在当前epoch是否active
		if helpers.IsActiveValidatorUsingTrie(val, currentEpoch) {
			pVal.IsActiveCurrentEpoch = true
			pBal.ActiveCurrentEpoch += val.EffectiveBalance()
		}
		// Was validator active previous epoch
		// validator在之前的epoch是否active
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
