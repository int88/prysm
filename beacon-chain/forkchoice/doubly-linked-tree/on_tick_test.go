package doublylinkedtree

import (
	"context"
	"testing"

	forkchoicetypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/forkchoice/types"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/testing/require"
)

func TestStore_NewSlot(t *testing.T) {
	ctx := context.Background()
	bj := [32]byte{'z'}

	type args struct {
		slot          types.Slot
		finalized     *forkchoicetypes.Checkpoint
		justified     *forkchoicetypes.Checkpoint
		bestJustified *forkchoicetypes.Checkpoint
		shouldEqual   bool
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "Not epoch boundary. No change",
			// 不是在epoch boundary，什么都不变更
			args: args{
				slot:          params.BeaconConfig().SlotsPerEpoch + 1,
				finalized:     &forkchoicetypes.Checkpoint{Epoch: 1, Root: [32]byte{'a'}},
				justified:     &forkchoicetypes.Checkpoint{Epoch: 2, Root: [32]byte{'b'}},
				bestJustified: &forkchoicetypes.Checkpoint{Epoch: 3, Root: bj},
				shouldEqual:   false,
			},
		},
		{
			name: "Justified higher than best justified. No change",
			// justified高于best justified，什么都不变
			args: args{
				slot:          params.BeaconConfig().SlotsPerEpoch,
				finalized:     &forkchoicetypes.Checkpoint{Epoch: 1, Root: [32]byte{'a'}},
				justified:     &forkchoicetypes.Checkpoint{Epoch: 3, Root: [32]byte{'b'}},
				bestJustified: &forkchoicetypes.Checkpoint{Epoch: 2, Root: bj},
				shouldEqual:   false,
			},
		},
		{
			name: "Best justified not on the same chain as finalized. No change",
			// best justified和finalized不在同一条链，不发生改变
			args: args{
				slot:          params.BeaconConfig().SlotsPerEpoch,
				finalized:     &forkchoicetypes.Checkpoint{Epoch: 1, Root: [32]byte{'a'}},
				justified:     &forkchoicetypes.Checkpoint{Epoch: 2, Root: [32]byte{'b'}},
				bestJustified: &forkchoicetypes.Checkpoint{Epoch: 3, Root: [32]byte{'d'}},
				shouldEqual:   false,
			},
		},
		{
			name: "Best justified on the same chain as finalized. Yes change",
			// best justified和finalized在同一条链，则发生改变
			args: args{
				slot:          params.BeaconConfig().SlotsPerEpoch,
				finalized:     &forkchoicetypes.Checkpoint{Epoch: 1, Root: [32]byte{'a'}},
				justified:     &forkchoicetypes.Checkpoint{Epoch: 2, Root: [32]byte{'b'}},
				bestJustified: &forkchoicetypes.Checkpoint{Epoch: 3, Root: bj},
				shouldEqual:   true,
			},
		},
	}
	for _, test := range tests {
		// 0, a, b
		// 0, a, bj
		// 0, d
		f := setup(test.args.justified.Epoch, test.args.finalized.Epoch)
		state, blkRoot, err := prepareForkchoiceState(ctx, 0, [32]byte{}, [32]byte{}, [32]byte{}, 0, 0)
		require.NoError(t, err)
		require.NoError(t, f.InsertNode(ctx, state, blkRoot)) // genesis
		state, blkRoot, err = prepareForkchoiceState(ctx, 32, [32]byte{'a'}, [32]byte{}, [32]byte{}, 0, 0)
		require.NoError(t, err)
		require.NoError(t, f.InsertNode(ctx, state, blkRoot)) // finalized
		state, blkRoot, err = prepareForkchoiceState(ctx, 64, [32]byte{'b'}, [32]byte{'a'}, [32]byte{}, 0, 0)
		require.NoError(t, err)
		require.NoError(t, f.InsertNode(ctx, state, blkRoot)) // justified
		state, blkRoot, err = prepareForkchoiceState(ctx, 96, bj, [32]byte{'a'}, [32]byte{}, 0, 0)
		require.NoError(t, err)
		require.NoError(t, f.InsertNode(ctx, state, blkRoot)) // best justified
		state, blkRoot, err = prepareForkchoiceState(ctx, 97, [32]byte{'d'}, [32]byte{}, [32]byte{}, 0, 0)
		require.NoError(t, err)
		require.NoError(t, f.InsertNode(ctx, state, blkRoot)) // bad

		// 更新finalized, justified以及best justified checkpoint
		require.NoError(t, f.UpdateFinalizedCheckpoint(test.args.finalized))
		require.NoError(t, f.UpdateJustifiedCheckpoint(test.args.justified))
		f.store.bestJustifiedCheckpoint = test.args.bestJustified

		require.NoError(t, f.NewSlot(ctx, test.args.slot))
		if test.args.shouldEqual {
			bcp := f.BestJustifiedCheckpoint()
			cp := f.JustifiedCheckpoint()
			// best justified和justified相等，包括epoch和root
			require.Equal(t, bcp.Epoch, cp.Epoch)
			require.Equal(t, bcp.Root, cp.Root)
		} else {
			bcp := f.BestJustifiedCheckpoint()
			cp := f.JustifiedCheckpoint()
			epochsEqual := bcp.Epoch == cp.Epoch
			rootsEqual := bcp.Root == cp.Root
			require.Equal(t, false, epochsEqual && rootsEqual)
		}
	}
}
