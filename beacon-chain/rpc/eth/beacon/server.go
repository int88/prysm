// Package beacon defines a gRPC beacon service implementation,
// beacon包定义了一个beacon service的gRPC实现
// following the official API standards https://ethereum.github.io/beacon-apis/#/.
// This package includes the beacon and config endpoints.
// 这个包包含了beacon以及config endpoints
package beacon

import (
	"github.com/prysmaticlabs/prysm/beacon-chain/blockchain"
	blockfeed "github.com/prysmaticlabs/prysm/beacon-chain/core/feed/block"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/feed/operation"
	"github.com/prysmaticlabs/prysm/beacon-chain/db"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/attestations"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/slashings"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/voluntaryexits"
	"github.com/prysmaticlabs/prysm/beacon-chain/p2p"
	v1alpha1validator "github.com/prysmaticlabs/prysm/beacon-chain/rpc/prysm/v1alpha1/validator"
	"github.com/prysmaticlabs/prysm/beacon-chain/rpc/statefetcher"
	"github.com/prysmaticlabs/prysm/beacon-chain/state/stategen"
	"github.com/prysmaticlabs/prysm/beacon-chain/sync"
)

// Server defines a server implementation of the gRPC Beacon Chain service,
// providing RPC endpoints to access data relevant to the Ethereum Beacon Chain.
// Server定义了一个gRPC Beacon Chain service的实现，提供RPC endpoints用于访问Ethereum
// Beacon Chain相关的数据
type Server struct {
	BeaconDB                db.ReadOnlyDatabase
	ChainInfoFetcher        blockchain.ChainInfoFetcher
	GenesisTimeFetcher      blockchain.TimeFetcher
	BlockReceiver           blockchain.BlockReceiver
	BlockNotifier           blockfeed.Notifier
	OperationNotifier       operation.Notifier
	Broadcaster             p2p.Broadcaster
	AttestationsPool        attestations.Pool
	SlashingsPool           slashings.PoolManager
	VoluntaryExitsPool      voluntaryexits.PoolManager
	StateGenService         stategen.StateManager
	StateFetcher            statefetcher.Fetcher
	HeadFetcher             blockchain.HeadFetcher
	OptimisticModeFetcher   blockchain.OptimisticModeFetcher
	V1Alpha1ValidatorServer *v1alpha1validator.Server
	SyncChecker             sync.Checker
	CanonicalHistory        *stategen.CanonicalHistory
	HeadUpdater             blockchain.HeadUpdater
}
