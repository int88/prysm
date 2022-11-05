// Package block contains types for block-specific events fired
// during the runtime of a beacon node.
// block包包含了block特定的events，在一个beacon node的运行时触发
package block

import "github.com/prysmaticlabs/prysm/consensus-types/interfaces"

const (
	// ReceivedBlock is sent after a block has been received by the beacon node via p2p or RPC.
	// ReceivedBlock被发送在一个block被beacon node通过p2p或者rpc接收之后
	ReceivedBlock = iota + 1
)

// ReceivedBlockData is the data sent with ReceivedBlock events.
type ReceivedBlockData struct {
	SignedBlock  interfaces.SignedBeaconBlock
	IsOptimistic bool
}
