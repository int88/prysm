package precompute

import "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"

// Validator stores the pre computation of individual validator's attesting records these records
// consist of attestation votes, block inclusion record. Pre computing and storing such record
// is essential for process epoch optimizations.
// Validator存储单个验证者attesting records的预计算，这些records包括attestation votes，block inclusion record
// 预计算和存储这样的记录对于process epoch优化至关重要
type Validator struct {
	// IsSlashed is true if the validator has been slashed.
	IsSlashed bool
	// IsWithdrawableCurrentEpoch is true if the validator can withdraw current epoch.
	IsWithdrawableCurrentEpoch bool
	// IsActiveCurrentEpoch is true if the validator was active current epoch.
	IsActiveCurrentEpoch bool
	// IsActivePrevEpoch is true if the validator was active prev epoch.
	IsActivePrevEpoch bool
	// IsCurrentEpochAttester is true if the validator attested current epoch.
	// IsCurrentEpochAttester为true，如果validator attested current epoch
	IsCurrentEpochAttester bool
	// IsCurrentEpochTargetAttester is true if the validator attested current epoch target.
	// IsCurrentEpochTargetAttester为true，如果validator attested current epoch target
	IsCurrentEpochTargetAttester bool
	// IsPrevEpochAttester is true if the validator attested previous epoch.
	IsPrevEpochAttester bool
	// IsPrevEpochSourceAttester is true if the validator attested to source previous epoch. [Only for Altair]
	IsPrevEpochSourceAttester bool
	// IsPrevEpochTargetAttester is true if the validator attested previous epoch target.
	IsPrevEpochTargetAttester bool
	// IsHeadAttester is true if the validator attested head.
	IsPrevEpochHeadAttester bool

	// CurrentEpochEffectiveBalance is how much effective balance this validator has current epoch.
	// CurrentEpochEffectiveBalance是该验证者在当前epoch的effective balance
	CurrentEpochEffectiveBalance uint64
	// InclusionSlot is the slot of when the attestation gets included in the chain.
	InclusionSlot primitives.Slot
	// InclusionDistance is the distance between the assigned slot and this validator's attestation was included in block.
	InclusionDistance primitives.Slot
	// ProposerIndex is the index of proposer at slot where this validator's attestation was included.
	ProposerIndex primitives.ValidatorIndex
	// BeforeEpochTransitionBalance is the validator balance prior to epoch transition.
	// BeforeEpochTransitionBalance是epoch transition之前的validator balance
	BeforeEpochTransitionBalance uint64
	// AfterEpochTransitionBalance is the validator balance after epoch transition.
	// AfterEpochTransitionBalance是epoch transition之后的validator balance
	AfterEpochTransitionBalance uint64

	// InactivityScore of the validator. [New in Altair]
	// 这个validator的InactivityScore
	InactivityScore uint64
}

// Balance stores the pre computation of the total participated balances for a given epoch
// Pre computing and storing such record is essential for process epoch optimizations.
// Balance存储给定epoch的总参与balance的预计算，预计算和存储这样的记录对于process epoch优化至关重要
type Balance struct {
	// ActiveCurrentEpoch is the total effective balance of all active validators during current epoch.
	// ActiveCurrentEpoch是当前epoch所有active validators的总effective balance
	ActiveCurrentEpoch uint64
	// ActivePrevEpoch is the total effective balance of all active validators during prev epoch.
	// ActivePrevEpoch是prev epoch所有active validators的总effective balance
	ActivePrevEpoch uint64
	// CurrentEpochAttested is the total effective balance of all validators who attested during current epoch.
	// CurrentEpochAttested是当前epoch所有attested的validators的总effective balance
	CurrentEpochAttested uint64
	// CurrentEpochTargetAttested is the total effective balance of all validators who attested
	// for epoch boundary block during current epoch.
	// CurrentEpochTargetAttested是当前epoch所有attested epoch boundary block的validators的总effective balance
	CurrentEpochTargetAttested uint64
	// PrevEpochAttested is the total effective balance of all validators who attested during prev epoch.
	PrevEpochAttested uint64
	// PrevEpochTargetAttested is the total effective balance of all validators who attested
	// for epoch boundary block during prev epoch.
	PrevEpochTargetAttested uint64
	// PrevEpochHeadAttested is the total effective balance of all validators who attested
	// correctly for head block during prev epoch.
	// PrevEpochHeadAttested是prev epoch所有attested head block的validators的总effective balance
	PrevEpochHeadAttested uint64
}
