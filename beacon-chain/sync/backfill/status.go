package backfill

import (
	"context"

	"github.com/pkg/errors"
	"github.com/prysmaticlabs/prysm/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/consensus-types/wrapper"
)

// NewStatus correctly initializes a Status value with the required database value.
// NewStatus正确地初始化一个Status的值，用要求的database值
func NewStatus(store BackfillDB) *Status {
	return &Status{
		store: store,
	}
}

// Status provides a way to update and query the status of a backfill process that may be necessary to track when
// a node was initialized via checkpoint sync. With checkpoint sync, there will be a gap in node history from genesis
// until the checkpoint sync origin block. Status provides the means to update the value keeping track of the lower
// end of the missing block range via the Advance() method, to check whether a Slot is missing from the database
// via the SlotCovered() method, and to see the current StartGap() and EndGap().
// Status提供了一个方法用于更新以及查询一个backfill process的status，可能在追踪一个node通过checkpoint sync初始化的时候有用
// 对于checkpoint sync，可能会有一个gap在node history中，从gensis到checkpoint sync origin block，Status提供了方法用于更新
// value追踪lower end of the missing block range，通过Advance()方法，用来检查是否一个Slot在database中缺失，通过SlotCovered()方法
// 以及查看当前的StartGap()和EndGap()
type Status struct {
	start       types.Slot
	end         types.Slot
	store       BackfillDB
	genesisSync bool
}

// SlotCovered uses StartGap() and EndGap() to determine if the given slot is covered by the current chain history.
// If the slot is <= StartGap(), or >= EndGap(), the result is true.
// If the slot is between StartGap() and EndGap(), the result is false.
func (s *Status) SlotCovered(sl types.Slot) bool {
	// short circuit if the node was synced from genesis
	if s.genesisSync {
		return true
	}
	if s.StartGap() < sl && sl < s.EndGap() {
		return false
	}
	return true
}

// StartGap returns the slot at the beginning of the range that needs to be backfilled.
func (s *Status) StartGap() types.Slot {
	return s.start
}

// EndGap returns the slot at the end of the range that needs to be backfilled.
func (s *Status) EndGap() types.Slot {
	return s.end
}

var ErrAdvancePastOrigin = errors.New("cannot advance backfill Status beyond the origin checkpoint slot")

// Advance advances the backfill position to the given slot & root.
// It updates the backfill block root entry in the database,
// and also updates the Status value's copy of the backfill position slot.
func (s *Status) Advance(ctx context.Context, upTo types.Slot, root [32]byte) error {
	if upTo > s.end {
		return errors.Wrapf(ErrAdvancePastOrigin, "advance slot=%d, origin slot=%d", upTo, s.end)
	}
	s.start = upTo
	return s.store.SaveBackfillBlockRoot(ctx, root)
}

// Reload queries the database for backfill status, initializing the internal data and validating the database state.
func (s *Status) Reload(ctx context.Context) error {
	cpRoot, err := s.store.OriginCheckpointBlockRoot(ctx)
	if err != nil {
		// mark genesis sync and short circuit further lookups
		if errors.Is(err, db.ErrNotFoundOriginBlockRoot) {
			s.genesisSync = true
			return nil
		}
		return err
	}
	cpBlock, err := s.store.Block(ctx, cpRoot)
	if err != nil {
		return errors.Wrapf(err, "error retrieving block for origin checkpoint root=%#x", cpRoot)
	}
	if err := wrapper.BeaconBlockIsNil(cpBlock); err != nil {
		return err
	}
	s.end = cpBlock.Block().Slot()

	_, err = s.store.GenesisBlockRoot(ctx)
	if err != nil {
		if errors.Is(err, db.ErrNotFoundGenesisBlockRoot) {
			return errors.Wrap(err, "genesis block root required for checkpoint sync")
		}
		return err
	}

	bfRoot, err := s.store.BackfillBlockRoot(ctx)
	if err != nil {
		if errors.Is(err, db.ErrNotFoundBackfillBlockRoot) {
			return errors.Wrap(err, "found origin checkpoint block root, but no backfill block root")
		}
		return err
	}
	bfBlock, err := s.store.Block(ctx, bfRoot)
	if err != nil {
		return errors.Wrapf(err, "error retrieving block for backfill root=%#x", bfRoot)
	}
	if err := wrapper.BeaconBlockIsNil(bfBlock); err != nil {
		return err
	}
	s.start = bfBlock.Block().Slot()
	return nil
}

// BackfillDB describes the set of DB methods that the Status type needs to function.
// BackfillDB描述了一系列DB的方法，Status类型需要作用
type BackfillDB interface {
	SaveBackfillBlockRoot(ctx context.Context, blockRoot [32]byte) error
	GenesisBlockRoot(ctx context.Context) ([32]byte, error)
	OriginCheckpointBlockRoot(ctx context.Context) ([32]byte, error)
	BackfillBlockRoot(ctx context.Context) ([32]byte, error)
	Block(ctx context.Context, blockRoot [32]byte) (interfaces.SignedBeaconBlock, error)
}
