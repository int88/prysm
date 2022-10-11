package evaluators

import (
	"context"
	"errors"

	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
	eth "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	e2etypes "github.com/prysmaticlabs/prysm/testing/endtoend/types"
	"google.golang.org/grpc"
)

// 必须大于46，32个hot states以及16个chkpt interval
const epochToCheck = 50 // must be more than 46 (32 hot states + 16 chkpt interval)

// ColdStateCheckpoint checks data from the database using cold state storage.
// ColdStateCheckpoint使用cold state storage从database检查数据
var ColdStateCheckpoint = e2etypes.Evaluator{
	Name: "cold_state_assignments_from_epoch_%d",
	Policy: func(currentEpoch types.Epoch) bool {
		return currentEpoch == epochToCheck
	},
	Evaluation: checkColdStateCheckpoint,
}

// Checks the first node for an old checkpoint using cold state storage.
// Checks使用cold state storage检查第一个node，对于一个old checkpoint
func checkColdStateCheckpoint(conns ...*grpc.ClientConn) error {
	ctx := context.Background()
	client := eth.NewBeaconChainClient(conns[0])

	for i := types.Epoch(0); i < epochToCheck; i++ {
		res, err := client.ListValidatorAssignments(ctx, &eth.ListValidatorAssignmentsRequest{
			QueryFilter: &eth.ListValidatorAssignmentsRequest_Epoch{Epoch: i},
		})
		if err != nil {
			return err
		}
		// A simple check to ensure we received some data.
		// 一个简单的检查来确保我们收到一些数据
		if res == nil || res.Epoch != i {
			// 对于一个old epoch返回validator assignments response失败，使用来自数据库的cold state storage
			return errors.New("failed to return a validator assignments response for an old epoch " +
				"using cold state storage from the database")
		}
	}

	return nil
}
