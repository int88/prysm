// Package beacon defines a gRPC beacon service implementation,
// following the official API standards https://ethereum.github.io/beacon-apis/#/.
// This package includes the beacon and config endpoints.
// beacon包定义了一个gRPC service的实现，遵循官方的API标准
// 这个包包含beacon以及config endpoints
package beacon

import (
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/blockchain"
	blockfeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/block"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/operation"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/execution"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/attestations"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/blstoexec"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/slashings"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/operations/voluntaryexits"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/p2p"
	v1alpha1validator "github.com/prysmaticlabs/prysm/v3/beacon-chain/rpc/prysm/v1alpha1/validator"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/rpc/statefetcher"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stategen"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/sync"
)

// Server defines a server implementation of the gRPC Beacon Chain service,
// providing RPC endpoints to access data relevant to the Ethereum Beacon Chain.
// Server定义了一个gRPC Beacon Chain service的server实现，提供RPC endpoints来访问Ethereum
// Beacon Chain相关的数据
type Server struct {
	BeaconDB                      db.ReadOnlyDatabase
	ChainInfoFetcher              blockchain.ChainInfoFetcher
	GenesisTimeFetcher            blockchain.TimeFetcher
	BlockReceiver                 blockchain.BlockReceiver
	BlockNotifier                 blockfeed.Notifier
	OperationNotifier             operation.Notifier
	Broadcaster                   p2p.Broadcaster
	AttestationsPool              attestations.Pool
	SlashingsPool                 slashings.PoolManager
	VoluntaryExitsPool            voluntaryexits.PoolManager
	StateGenService               stategen.StateManager
	StateFetcher                  statefetcher.Fetcher
	HeadFetcher                   blockchain.HeadFetcher
	OptimisticModeFetcher         blockchain.OptimisticModeFetcher
	V1Alpha1ValidatorServer       *v1alpha1validator.Server
	SyncChecker                   sync.Checker
	CanonicalHistory              *stategen.CanonicalHistory
	ExecutionPayloadReconstructor execution.ExecutionPayloadReconstructor
	FinalizationFetcher           blockchain.FinalizationFetcher
	BLSChangesPool                blstoexec.PoolManager
}
