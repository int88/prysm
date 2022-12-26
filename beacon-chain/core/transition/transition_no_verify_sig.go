package transition

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/altair"
	b "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/blocks"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition/interop"
	v "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/validators"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	"github.com/prysmaticlabs/prysm/v3/crypto/bls"
	"github.com/prysmaticlabs/prysm/v3/monitoring/tracing"
	"github.com/prysmaticlabs/prysm/v3/runtime/version"
	"go.opencensus.io/trace"
)

// ExecuteStateTransitionNoVerifyAnySig defines the procedure for a state transition function.
// This does not validate any BLS signatures of attestations, block proposer signature, randao signature,
// it is used for performing a state transition as quickly as possible. This function also returns a signature
// set of all signatures not verified, so that they can be stored and verified later.
// ExecuteStateTransitionNoVerifyAnySig定义了state transition功能，它不做任何校验，它用于尽快执行state transition
// 同时返回一系列没有校验的signatures，这样它们可以被存储并且后面被jioayan
//
// WARNING: This method does not validate any signatures (i.e. calling `state_transition()` with `validate_result=False`).
// This method also modifies the passed in state.
//
// Spec pseudocode definition:
//
//			def state_transition(state: BeaconState, signed_block: SignedBeaconBlock, validate_result: bool=True) -> None:
//			  block = signed_block.message
//			  # Process slots (including those with no blocks) since block
//		   # 处理slots（包括那些没有blocks的），从block以来
//			  process_slots(state, block.slot)
//			  # Verify signature
//			  if validate_result:
//			      assert verify_block_signature(state, signed_block)
//			  # Process block
//	       # 校验block
//			  process_block(state, block)
//			  # Verify state root
//	       # 校验state root
//			  if validate_result:
//			      assert block.state_root == hash_tree_root(state)
func ExecuteStateTransitionNoVerifyAnySig(
	ctx context.Context,
	st state.BeaconState,
	signed interfaces.SignedBeaconBlock,
) (*bls.SignatureBatch, state.BeaconState, error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if signed == nil || signed.IsNil() || signed.Block().IsNil() {
		return nil, nil, errors.New("nil block")
	}

	ctx, span := trace.StartSpan(ctx, "core.state.ExecuteStateTransitionNoVerifyAttSigs")
	defer span.End()
	var err error

	interop.WriteBlockToDisk(signed, false /* Has the block failed */)
	interop.WriteStateToDisk(st)

	parentRoot := signed.Block().ParentRoot()
	// 处理Slots，使用下一个Slot Cache
	st, err = ProcessSlotsUsingNextSlotCache(ctx, st, parentRoot[:], signed.Block().Slot())
	if err != nil {
		return nil, nil, errors.Wrap(err, "could not process slots")
	}

	// Execute per block transition.
	// 执行每个block的transition
	set, st, err := ProcessBlockNoVerifyAnySig(ctx, st, signed)
	if err != nil {
		return nil, nil, errors.Wrap(err, "could not process block")
	}

	// State root validation.
	// 对state root进行校验
	postStateRoot, err := st.HashTreeRoot(ctx)
	if err != nil {
		return nil, nil, err
	}
	stateRoot := signed.Block().StateRoot()
	if !bytes.Equal(postStateRoot[:], stateRoot[:]) {
		// 不能校验state root
		return nil, nil, fmt.Errorf("could not validate state root, wanted: %#x, received: %#x",
			postStateRoot[:], signed.Block().StateRoot())
	}

	return set, st, nil
}

// CalculateStateRoot defines the procedure for a state transition function.
// This does not validate any BLS signatures in a block, it is used for calculating the
// state root of the state for the block proposer to use.
// This does not modify state.
//
// WARNING: This method does not validate any BLS signatures (i.e. calling `state_transition()` with `validate_result=False`).
// This is used for proposer to compute state root before proposing a new block, and this does not modify state.
//
// Spec pseudocode definition:
//
//	def state_transition(state: BeaconState, signed_block: SignedBeaconBlock, validate_result: bool=True) -> None:
//	  block = signed_block.message
//	  # Process slots (including those with no blocks) since block
//	  process_slots(state, block.slot)
//	  # Verify signature
//	  if validate_result:
//	      assert verify_block_signature(state, signed_block)
//	  # Process block
//	  process_block(state, block)
//	  # Verify state root
//	  if validate_result:
//	      assert block.state_root == hash_tree_root(state)
func CalculateStateRoot(
	ctx context.Context,
	state state.BeaconState,
	signed interfaces.SignedBeaconBlock,
) ([32]byte, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.CalculateStateRoot")
	defer span.End()
	if ctx.Err() != nil {
		tracing.AnnotateError(span, ctx.Err())
		return [32]byte{}, ctx.Err()
	}
	if state == nil || state.IsNil() {
		return [32]byte{}, errors.New("nil state")
	}
	if signed == nil || signed.IsNil() || signed.Block().IsNil() {
		return [32]byte{}, errors.New("nil block")
	}

	// Copy state to avoid mutating the state reference.
	state = state.Copy()

	// Execute per slots transition.
	var err error
	parentRoot := signed.Block().ParentRoot()
	state, err = ProcessSlotsUsingNextSlotCache(ctx, state, parentRoot[:], signed.Block().Slot())
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "could not process slots")
	}

	// Execute per block transition.
	state, err = ProcessBlockForStateRoot(ctx, state, signed)
	if err != nil {
		return [32]byte{}, errors.Wrap(err, "could not process block")
	}

	return state.HashTreeRoot(ctx)
}

// ProcessBlockNoVerifyAnySig creates a new, modified beacon state by applying block operation
// transformations as defined in the Ethereum Serenity specification. It does not validate
// any block signature except for deposit and slashing signatures. It also returns the relevant
// signature set from all the respective methods.
// ProcessBlockNoVerifyAnySig创建一个新的，修改后的beacon state，通过应用定义在Ethereum Serenity specification
// 中的block operation transformation，它不校验任何的block signature，出列deposit以及slashing signatures
// 它同时返回相关的signature set，从所有相关的方法
//
// Spec pseudocode definition:
//
//	def process_block(state: BeaconState, block: BeaconBlock) -> None:
//	  process_block_header(state, block)
//	  process_randao(state, block.body)
//	  process_eth1_data(state, block.body)
//	  process_operations(state, block.body)
func ProcessBlockNoVerifyAnySig(
	ctx context.Context,
	st state.BeaconState,
	signed interfaces.SignedBeaconBlock,
) (*bls.SignatureBatch, state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessBlockNoVerifyAnySig")
	defer span.End()
	if err := blocks.BeaconBlockIsNil(signed); err != nil {
		return nil, nil, err
	}

	if st.Version() != signed.Block().Version() {
		// 如果state和block是不同的版本，直接报错
		return nil, nil, fmt.Errorf("state and block are different version. %d != %d", st.Version(), signed.Block().Version())
	}

	blk := signed.Block()
	// 处理block用于state root
	st, err := ProcessBlockForStateRoot(ctx, st, signed)
	if err != nil {
		return nil, nil, err
	}

	sig := signed.Signature()
	bSet, err := b.BlockSignatureBatch(st, blk.ProposerIndex(), sig[:], blk.HashTreeRoot)
	if err != nil {
		tracing.AnnotateError(span, err)
		// 不能获取block signature set
		return nil, nil, errors.Wrap(err, "could not retrieve block signature set")
	}
	randaoReveal := signed.Block().Body().RandaoReveal()
	rSet, err := b.RandaoSignatureBatch(ctx, st, randaoReveal[:])
	if err != nil {
		tracing.AnnotateError(span, err)
		// 不能获取randao signature set
		return nil, nil, errors.Wrap(err, "could not retrieve randao signature set")
	}
	aSet, err := b.AttestationSignatureBatch(ctx, st, signed.Block().Body().Attestations())
	if err != nil {
		// 不能获取attestation signature set
		return nil, nil, errors.Wrap(err, "could not retrieve attestation signature set")
	}

	// Merge beacon block, randao and attestations signatures into a set.
	// 合并beacon block，randao以及attestations signatures到一个set
	set := bls.NewSet()
	set.Join(bSet).Join(rSet).Join(aSet)

	if blk.Version() >= version.Capella {
		changes, err := signed.Block().Body().BLSToExecutionChanges()
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not get BLSToExecutionChanges")
		}
		cSet, err := b.BLSChangesSignatureBatch(ctx, st, changes)
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not get BLSToExecutionChanges signatures")
		}
		set.Join(cSet)
	}
	return set, st, nil
}

// ProcessOperationsNoVerifyAttsSigs processes the operations in the beacon block and updates beacon state
// with the operations in block. It does not verify attestation signatures.
// ProcessOperationsNoVerifyAttsSigs处理beacon block中的operations并且更新beacon state，用block中的operations
// 它不校验attestation signatures
//
// WARNING: This method does not verify attestation signatures.
// This is used to perform the block operations as fast as possible.
//
// Spec pseudocode definition:
//
//	def process_operations(state: BeaconState, body: BeaconBlockBody) -> None:
//	  # Verify that outstanding deposits are processed up to the maximum number of deposits
//	  assert len(body.deposits) == min(MAX_DEPOSITS, state.eth1_data.deposit_count - state.eth1_deposit_index)
//
//	  def for_ops(operations: Sequence[Any], fn: Callable[[BeaconState, Any], None]) -> None:
//	      for operation in operations:
//	          fn(state, operation)
//
//	  for_ops(body.proposer_slashings, process_proposer_slashing)
//	  for_ops(body.attester_slashings, process_attester_slashing)
//	  for_ops(body.attestations, process_attestation)
//	  for_ops(body.deposits, process_deposit)
//	  for_ops(body.voluntary_exits, process_voluntary_exit)
func ProcessOperationsNoVerifyAttsSigs(
	ctx context.Context,
	state state.BeaconState,
	signedBeaconBlock interfaces.SignedBeaconBlock) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessOperationsNoVerifyAttsSigs")
	defer span.End()
	if err := blocks.BeaconBlockIsNil(signedBeaconBlock); err != nil {
		return nil, err
	}

	if _, err := VerifyOperationLengths(ctx, state, signedBeaconBlock); err != nil {
		return nil, errors.Wrap(err, "could not verify operation lengths")
	}

	var err error
	switch signedBeaconBlock.Version() {
	case version.Phase0:
		state, err = phase0Operations(ctx, state, signedBeaconBlock)
		if err != nil {
			return nil, err
		}
	case version.Altair, version.Bellatrix, version.Capella:
		state, err = altairOperations(ctx, state, signedBeaconBlock)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("block does not have correct version")
	}

	return state, nil
}

// ProcessBlockForStateRoot processes the state for state root computation. It skips proposer signature
// and randao signature verifications.
// ProcessBlockForStateRoot处理state，对于state root的计算
//
// Spec pseudocode definition:
// def process_block(state: BeaconState, block: BeaconBlock) -> None:
//
//		 处理block header
//			process_block_header(state, block)
//			if is_execution_enabled(state, block.body):
//			    process_execution_payload(state, block.body.execution_payload, EXECUTION_ENGINE)  # [New in Bellatrix]
//			process_randao(state, block.body)
//	     处理eth1 data
//			process_eth1_data(state, block.body)
//			process_operations(state, block.body)
//			process_sync_aggregate(state, block.body.sync_aggregate)
func ProcessBlockForStateRoot(
	ctx context.Context,
	state state.BeaconState,
	signed interfaces.SignedBeaconBlock,
) (state.BeaconState, error) {
	ctx, span := trace.StartSpan(ctx, "core.state.ProcessBlockForStateRoot")
	defer span.End()
	if err := blocks.BeaconBlockIsNil(signed); err != nil {
		return nil, err
	}

	blk := signed.Block()
	body := blk.Body()
	bodyRoot, err := body.HashTreeRoot()
	if err != nil {
		return nil, errors.Wrap(err, "could not hash tree root beacon block body")
	}
	parentRoot := blk.ParentRoot()
	// 处理block header
	state, err = b.ProcessBlockHeaderNoVerify(ctx, state, blk.Slot(), blk.ProposerIndex(), parentRoot[:], bodyRoot[:])
	if err != nil {
		tracing.AnnotateError(span, err)
		return nil, errors.Wrap(err, "could not process block header")
	}

	// 检查execution是否使能
	enabled, err := b.IsExecutionEnabled(state, blk.Body())
	if err != nil {
		return nil, errors.Wrap(err, "could not check if execution is enabled")
	}
	if enabled {
		executionData, err := blk.Body().Execution()
		if err != nil {
			return nil, err
		}
		if blk.IsBlinded() {
			// 处理payload header
			state, err = b.ProcessPayloadHeader(state, executionData)
		} else {
			// 处理payload
			state, err = b.ProcessPayload(state, executionData)
		}
		if err != nil {
			return nil, errors.Wrap(err, "could not process execution data")
		}
	}

	randaoReveal := signed.Block().Body().RandaoReveal()
	// 处理randao
	state, err = b.ProcessRandaoNoVerify(state, randaoReveal[:])
	if err != nil {
		tracing.AnnotateError(span, err)
		return nil, errors.Wrap(err, "could not verify and process randao")
	}

	// 处理eth1 data
	state, err = b.ProcessEth1DataInBlock(ctx, state, signed.Block().Body().Eth1Data())
	if err != nil {
		tracing.AnnotateError(span, err)
		return nil, errors.Wrap(err, "could not process eth1 data")
	}

	// 处理block operation
	state, err = ProcessOperationsNoVerifyAttsSigs(ctx, state, signed)
	if err != nil {
		tracing.AnnotateError(span, err)
		return nil, errors.Wrap(err, "could not process block operation")
	}

	if signed.Block().Version() == version.Phase0 {
		return state, nil
	}

	sa, err := signed.Block().Body().SyncAggregate()
	if err != nil {
		// 不能从block中获取sync aggregate
		return nil, errors.Wrap(err, "could not get sync aggregate from block")
	}
	// 处理sync aggregate
	state, err = altair.ProcessSyncAggregate(ctx, state, sa)
	if err != nil {
		return nil, errors.Wrap(err, "process_sync_aggregate failed")
	}

	return state, nil
}

// This calls altair block operations.
func altairOperations(
	ctx context.Context,
	st state.BeaconState,
	signedBeaconBlock interfaces.SignedBeaconBlock) (state.BeaconState, error) {
	// 处理proposer slashings
	st, err := b.ProcessProposerSlashings(ctx, st, signedBeaconBlock.Block().Body().ProposerSlashings(), v.SlashValidator)
	if err != nil {
		return nil, errors.Wrap(err, "could not process altair proposer slashing")
	}
	// 处理attester slashings
	st, err = b.ProcessAttesterSlashings(ctx, st, signedBeaconBlock.Block().Body().AttesterSlashings(), v.SlashValidator)
	if err != nil {
		return nil, errors.Wrap(err, "could not process altair attester slashing")
	}
	// 处理attestations
	st, err = altair.ProcessAttestationsNoVerifySignature(ctx, st, signedBeaconBlock)
	if err != nil {
		return nil, errors.Wrap(err, "could not process altair attestation")
	}
	// 处理deposits
	if _, err := altair.ProcessDeposits(ctx, st, signedBeaconBlock.Block().Body().Deposits()); err != nil {
		return nil, errors.Wrap(err, "could not process altair deposit")
	}
	// 处理voluntary exits
	st, err = b.ProcessVoluntaryExits(ctx, st, signedBeaconBlock.Block().Body().VoluntaryExits())
	if err != nil {
		return nil, errors.Wrap(err, "could not process voluntary exits")
	}
	return b.ProcessBLSToExecutionChanges(st, signedBeaconBlock)
}

// This calls phase 0 block operations.
// 调用phase 0阶段的block operations
func phase0Operations(
	ctx context.Context,
	st state.BeaconState,
	signedBeaconBlock interfaces.SignedBeaconBlock) (state.BeaconState, error) {
	// 处理proposer slashings
	st, err := b.ProcessProposerSlashings(ctx, st, signedBeaconBlock.Block().Body().ProposerSlashings(), v.SlashValidator)
	if err != nil {
		return nil, errors.Wrap(err, "could not process block proposer slashings")
	}
	// 处理attester slashings
	st, err = b.ProcessAttesterSlashings(ctx, st, signedBeaconBlock.Block().Body().AttesterSlashings(), v.SlashValidator)
	if err != nil {
		return nil, errors.Wrap(err, "could not process block attester slashings")
	}
	// 处理block attestations
	st, err = b.ProcessAttestationsNoVerifySignature(ctx, st, signedBeaconBlock)
	if err != nil {
		return nil, errors.Wrap(err, "could not process block attestations")
	}
	// 处理block deposits
	if _, err := b.ProcessDeposits(ctx, st, signedBeaconBlock.Block().Body().Deposits()); err != nil {
		return nil, errors.Wrap(err, "could not process deposits")
	}
	// 最后处理voluntary exits
	return b.ProcessVoluntaryExits(ctx, st, signedBeaconBlock.Block().Body().VoluntaryExits())
}
