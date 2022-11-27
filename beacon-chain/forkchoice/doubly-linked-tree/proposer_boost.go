package doublylinkedtree

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/config/params"
)

// ResetBoostedProposerRoot sets the value of the proposer boosted root to zeros.
// ResetBoostedProposerRoot设置proposer boosted root的值到0
func (f *ForkChoice) ResetBoostedProposerRoot(_ context.Context) error {
	f.store.proposerBoostLock.Lock()
	f.store.proposerBoostRoot = [32]byte{}
	f.store.proposerBoostLock.Unlock()
	return nil
}

// Given a list of validator balances, we compute the proposer boost score
// that should be given to a proposer based on their committee weight, derived from
// the total active balances, the size of a committee, and a boost score constant.
// 给定一系列的validator balances，我们计算proposer boost score，应该给一个proposer，基于它们的
// committee weight，继承自一个完整的active balances，一个committee的大小以及一个boost score常量
// IMPORTANT: The caller MUST pass in a list of validator balances where balances > 0 refer to active
// validators while balances == 0 are for inactive validators.
// 注意：调用者必须传入一系列的validator balances，其中balances > 0，则为active validators
// 当balances == 0时，则为inactive validators
func computeProposerBoostScore(validatorBalances []uint64) (score uint64, err error) {
	totalActiveBalance := uint64(0)
	numActive := uint64(0)
	for _, balance := range validatorBalances {
		// We only consider balances > 0. The input slice should be constructed
		// as balance > 0 for all active validators and 0 for inactive ones.
		if balance == 0 {
			continue
		}
		totalActiveBalance += balance
		numActive += 1
	}
	if numActive == 0 {
		// Should never happen.
		// 这不应该发生
		err = errors.New("no active validators")
		return
	}
	committeeWeight := totalActiveBalance / uint64(params.BeaconConfig().SlotsPerEpoch)
	// 计算score
	score = (committeeWeight * params.BeaconConfig().ProposerScoreBoost) / 100
	return
}
