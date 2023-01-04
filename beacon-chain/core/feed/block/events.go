// Package block contains types for block-specific events fired
// during the runtime of a beacon node.
// block包包含了block特定的events，在一个beacon node的运行时触发
package block

import "github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"

const (
	// ReceivedBlock is sent after a block has been received by the beacon node via p2p or RPC.
	// ReceivedBlock在一个block通过p2p或者RPC被beacon node接收到之后被调用
	ReceivedBlock = iota + 1
)

// ReceivedBlockData is the data sent with ReceivedBlock events.
type ReceivedBlockData struct {
	SignedBlock  interfaces.SignedBeaconBlock
	IsOptimistic bool
}
