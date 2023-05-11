package stateutil

import (
	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/math"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
)

// UnrealizedCheckpointBalances returns the total current active balance, the
// total previous epoch correctly attested for target balance, and the total
// current epoch correctly attested for target balance. It takes the current and
// previous epoch participation bits as parameters so implicitly only works for
// beacon states post-Altair.
// UnrealizedCheckpointBalances返回总的当前active余额，正确attested的上一个epoch的target余额，以及正确attested的当前epoch的target余额
// 它以当前和上一个epoch的participation bits作为参数，因此隐式地只适用于Altair之后的beacon states
func UnrealizedCheckpointBalances(cp, pp []byte, validators []*ethpb.Validator, currentEpoch primitives.Epoch) (uint64, uint64, uint64, error) {
	targetIdx := params.BeaconConfig().TimelyTargetFlagIndex
	activeBalance := uint64(0)
	currentTarget := uint64(0)
	prevTarget := uint64(0)
	if len(cp) < len(validators) || len(pp) < len(validators) {
		return 0, 0, 0, errors.New("participation does not match validator set")
	}

	var err error
	for i, v := range validators {
		// 依然遍历了各个validators
		active := v.ActivationEpoch <= currentEpoch && currentEpoch < v.ExitEpoch
		if active && !v.Slashed {
			// 获取active balance
			activeBalance, err = math.Add64(activeBalance, v.EffectiveBalance)
			if err != nil {
				return 0, 0, 0, err
			}
			if ((cp[i] >> targetIdx) & 1) == 1 {
				// 添加到current target
				currentTarget, err = math.Add64(currentTarget, v.EffectiveBalance)
				if err != nil {
					return 0, 0, 0, err
				}
			}
			if ((pp[i] >> targetIdx) & 1) == 1 {
				// 添加到prev target
				prevTarget, err = math.Add64(prevTarget, v.EffectiveBalance)
				if err != nil {
					return 0, 0, 0, err
				}
			}
		}
	}
	return activeBalance, prevTarget, currentTarget, nil
}
