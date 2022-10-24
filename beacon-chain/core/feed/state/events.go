// Package state contains types for state operation-specific events fired
// during the runtime of a beacon node such state initialization, state updates,
// and chain start.
package state

import (
	"time"

	"github.com/prysmaticlabs/prysm/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/consensus-types/primitives"
)

const (
	// BlockProcessed is sent after a block has been processed and updated the state database.
	// BlockProcessed被发送，在一个block已经被处理并且更新state database
	BlockProcessed = iota + 1
	// ChainStarted is sent when enough validators are active to start proposing blocks.
	// ChainStarted被发送，当有足够的validators处于active能够开始proposing blocks
	ChainStarted
	// Initialized is sent when the internal beacon node's state is ready to be accessed.
	// Initialized被发送，当internal beacon node的state已经准备好被访问
	Initialized
	// Synced is sent when the beacon node has completed syncing and is ready to participate in the network.
	// Synced被发送，当beacon node已经完成同步并且准备好加入到network中
	Synced
	// Reorg is an event sent when the new head state's slot after a block
	// transition is lower than its previous head state slot value.
	// Reorg是一个事件发送，当新的head state的slot在一个block transition之后小于它之前的
	// head state的slot value
	Reorg
	// FinalizedCheckpoint event.
	FinalizedCheckpoint
	// NewHead of the chain event.
	NewHead
)

// BlockProcessedData is the data sent with BlockProcessed events.
type BlockProcessedData struct {
	// Slot is the slot of the processed block.
	Slot types.Slot
	// BlockRoot of the processed block.
	BlockRoot [32]byte
	// SignedBlock is the physical processed block.
	SignedBlock interfaces.SignedBeaconBlock
	// Verified is true if the block's BLS contents have been verified.
	Verified bool
}

// ChainStartedData is the data sent with ChainStarted events.
type ChainStartedData struct {
	// StartTime is the time at which the chain started.
	StartTime time.Time
}

// SyncedData is the data sent with Synced events.
type SyncedData struct {
	// StartTime is the time at which the chain started.
	StartTime time.Time
}

// InitializedData is the data sent with Initialized events.
type InitializedData struct {
	// StartTime is the time at which the chain started.
	StartTime time.Time
	// GenesisValidatorsRoot represents state.validators.HashTreeRoot().
	GenesisValidatorsRoot []byte
}
