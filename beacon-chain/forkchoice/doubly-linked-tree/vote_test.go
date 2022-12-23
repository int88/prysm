package doublylinkedtree

import (
	"context"
	"testing"

	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/testing/assert"
	"github.com/prysmaticlabs/prysm/v3/testing/require"
)

func TestVotes_CanFindHead(t *testing.T) {
	balances := []uint64{1, 1}
	f := setup(1, 1)
	ctx := context.Background()

	// The head should always start at the finalized block.
	// head总是应该从finalized block开始
	r, err := f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, params.BeaconConfig().ZeroHash, r, "Incorrect head with genesis")

	// Insert block 2 into the tree and verify head is at 2:
	//         0
	//        /
	//       2 <- head
	state, blkRoot, err := prepareForkchoiceState(context.Background(), 0, indexToHash(2), params.BeaconConfig().ZeroHash, params.BeaconConfig().ZeroHash, 1, 1)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(2), r, "Incorrect head for with justified epoch at 1")

	// Insert block 1 into the tree and verify head is still at 2:
	//            0
	//           / \
	//  head -> 2  1
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(1), params.BeaconConfig().ZeroHash, params.BeaconConfig().ZeroHash, 1, 1)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(2), r, "Incorrect head for with justified epoch at 1")

	// Add a vote to block 1 of the tree and verify head is switched to 1:
	// 添加一个vote到block 1，校验head已经转换到1
	//            0
	//           / \
	//          2  1 <- +vote, new head
	f.ProcessAttestation(context.Background(), []uint64{0}, indexToHash(1), 2)
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(1), r, "Incorrect head for with justified epoch at 1")

	// Add a vote to block 2 of the tree and verify head is switched to 2:
	// 添加一个vote到block 2，校验head转换为2
	//                     0
	//                    / \
	// vote, new head -> 2   1
	f.ProcessAttestation(context.Background(), []uint64{1}, indexToHash(2), 2)
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(2), r, "Incorrect head for with justified epoch at 1")

	// Insert block 3 into the tree and verify head is still at 2:
	// 插入block 3到tree中并且校验head还是2
	//            0
	//           / \
	//  head -> 2  1
	//             |
	//             3
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(3), indexToHash(1), params.BeaconConfig().ZeroHash, 1, 1)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(2), r, "Incorrect head for with justified epoch at 1")

	// Move validator 0's vote from 1 to 3 and verify head is still at 2:
	// 移动validator 0的vote从1到3并且校验head依然在2
	//            0
	//           / \
	//  head -> 2  1 <- old vote
	//             |
	//             3 <- new vote
	f.ProcessAttestation(context.Background(), []uint64{0}, indexToHash(3), 3)
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(2), r, "Incorrect head for with justified epoch at 1")

	// Move validator 1's vote from 2 to 1 and verify head is switched to 3:
	// 将validator 1的vote从2移动到1并且校验head切换到3
	//               0
	//              / \
	// old vote -> 2  1 <- new vote
	//                |
	//                3 <- head
	f.ProcessAttestation(context.Background(), []uint64{1}, indexToHash(1), 3)
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(3), r, "Incorrect head for with justified epoch at 1")

	// Insert block 4 into the tree and verify head is at 4:
	// 插入block 4到tree并校验head是4
	//            0
	//           / \
	//          2  1
	//             |
	//             3
	//             |
	//             4 <- head
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(4), indexToHash(3), params.BeaconConfig().ZeroHash, 1, 1)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(4), r, "Incorrect head for with justified epoch at 1")

	// Insert block 5 with justified epoch 2, it becomes head
	// 插入block 5，其中justified epoch为2，它变为head
	//            0
	//           / \
	//          2  1
	//             |
	//             3
	//             |
	//             4 <- head
	//            /
	//           5 <- justified epoch = 2
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(5), indexToHash(4), params.BeaconConfig().ZeroHash, 2, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(5), r, "Incorrect head for with justified epoch at 1")

	// Insert block 6 with justified epoch 3: verify it's head
	// 插入block 6，并且justified epoch为3，校验它是head
	//            0
	//           / \
	//          2  1
	//             |
	//             3
	//             |
	//             4 <- head
	//            / \
	//           5  6 <- justified epoch = 3
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(6), indexToHash(4), params.BeaconConfig().ZeroHash, 3, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(6), r, "Incorrect head for with justified epoch at 1")

	// Moved 2 votes to block 5:
	// 移动2个votes到block 5
	f.ProcessAttestation(context.Background(), []uint64{0, 1}, indexToHash(5), 4)

	// Inset blocks 7 and 8
	// 6 should still be the head, even though 5 has all the votes.
	// 插入blocks 7和8,6依然是head，即使5有所有的votes
	//            0
	//           / \
	//          2  1
	//             |
	//             3
	//             |
	//             4
	//            / \
	//           5  6 <- head
	//           |
	//           7
	//           |
	//           8
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(7), indexToHash(5), params.BeaconConfig().ZeroHash, 2, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(8), indexToHash(7), params.BeaconConfig().ZeroHash, 2, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(6), r, "Incorrect head for with justified epoch at 1")

	// Insert block 9 with justified epoch 3, it becomes head
	// Verify 9 is the head:
	// 插入block 9，有着justified epoch 3，它变为head
	//            0
	//           / \
	//          2  1
	//             |
	//             3
	//             |
	//             4
	//            / \
	//           5  6
	//           |
	//           7
	//           |
	//           8
	//           |
	//           10 <- head
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(10), indexToHash(8), params.BeaconConfig().ZeroHash, 3, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(10), r, "Incorrect head for with justified epoch at 3")

	// Insert block 9 forking 10 verify it's head (lexicographic order)
	//             0
	//            / \
	//           2  1
	//              |
	//              3
	//              |
	//              4
	//             / \
	//            5  6
	//            |
	//            7
	//            |
	//            8
	//           / \
	//	    9  10
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(9), indexToHash(8), params.BeaconConfig().ZeroHash, 3, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(9), r, "Incorrect head for with justified epoch at 3")

	// Move two votes for 10, verify it's head
	// 将两个votes移动到10，校验它变为head

	f.ProcessAttestation(context.Background(), []uint64{0, 1}, indexToHash(10), 5)
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(10), r, "Incorrect head for with justified epoch at 3")

	// Add 3 more validators to the system.
	// 添加3个validators到系统中
	balances = []uint64{1, 1, 1, 1, 1}
	// The new validators voted for 9
	// 新的validators投票给9
	f.ProcessAttestation(context.Background(), []uint64{2, 3, 4}, indexToHash(9), 5)
	// The new head should be 9.
	// 新的head应该变为9
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(9), r, "Incorrect head for with justified epoch at 3")

	// Set the balances of the last 2 validators to 0.
	// 设置最后两个validators的balances为0
	balances = []uint64{1, 1, 1, 0, 0}
	// The head should be back to 10.
	// head应该重新回到10
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(10), r, "Incorrect head for with justified epoch at 3")

	// Set the balances back to normal.
	// 将balances重回正常
	balances = []uint64{1, 1, 1, 1, 1}
	// The head should be back to 9.
	// head应该回到9
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(9), r, "Incorrect head for with justified epoch at 3")

	// Remove the last 2 validators.
	// 移除最后两个validators
	balances = []uint64{1, 1, 1}
	// The head should be back to 10.
	// head应该重新回到10
	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(10), r, "Incorrect head for with justified epoch at 3")

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(10), r, "Incorrect head for with justified epoch at 3")

	// Verify pruning above the prune threshold does prune:
	// 校验prune threshold之上的pruning真的会进行prune
	//          0
	//         / \
	//        2   1
	//            |
	//            3
	//            |
	//            4
	// -------pruned here ------
	//          5   6
	//          |
	//          7
	//          |
	//          8
	//         / \
	//        9  10
	// 设置5为finalizedCheckpoint root
	f.store.finalizedCheckpoint.Root = indexToHash(5)
	require.NoError(t, f.store.prune(context.Background()))
	assert.Equal(t, 5, len(f.store.nodeByRoot), "Incorrect nodes length after prune")
	// we pruned artificially the justified root.
	f.store.justifiedCheckpoint.Root = indexToHash(5)

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(10), r, "Incorrect head for with justified epoch at 2")

	// Insert new block 11 and verify head is at 11.
	// 插入新的block 11并且校验head是11
	//          5   6
	//          |
	//          7
	//          |
	//          8
	//         / \
	//        10  9
	//        |
	// head-> 11
	state, blkRoot, err = prepareForkchoiceState(context.Background(), 0, indexToHash(11), indexToHash(10), params.BeaconConfig().ZeroHash, 3, 2)
	require.NoError(t, err)
	require.NoError(t, f.InsertNode(ctx, state, blkRoot))

	r, err = f.Head(context.Background(), balances)
	require.NoError(t, err)
	assert.Equal(t, indexToHash(11), r, "Incorrect head for with justified epoch at 3")
}
