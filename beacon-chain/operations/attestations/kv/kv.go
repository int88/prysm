// Package kv includes a key-value store implementation
// of an attestation cache used to satisfy important use-cases
// such as aggregation in a beacon node runtime.
package kv

import (
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/prysmaticlabs/prysm/config/params"
	"github.com/prysmaticlabs/prysm/crypto/hash"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
)

var hashFn = hash.HashProto

// AttCaches defines the caches used to satisfy attestation pool interface.
// AttCaches定义了缓存用于满足attestation pool的接口
// These caches are KV store for various attestations
// 这些caches是KV store用于各种attestations，例如unaggregated, aggregated或者一个block内的attestations
// such are unaggregated, aggregated or attestations within a block.
type AttCaches struct {
	aggregatedAttLock  sync.RWMutex
	aggregatedAtt      map[[32]byte][]*ethpb.Attestation
	unAggregateAttLock sync.RWMutex
	unAggregatedAtt    map[[32]byte]*ethpb.Attestation
	forkchoiceAttLock  sync.RWMutex
	forkchoiceAtt      map[[32]byte]*ethpb.Attestation
	blockAttLock       sync.RWMutex
	blockAtt           map[[32]byte][]*ethpb.Attestation
	seenAtt            *cache.Cache
}

// NewAttCaches initializes a new attestation pool consists of multiple KV store in cache for
// various kind of attestations.
// NewAttCaches初始化一个新的attestation pool，由缓存中的多个KV store构成，对于各种的attestations
func NewAttCaches() *AttCaches {
	secsInEpoch := time.Duration(params.BeaconConfig().SlotsPerEpoch.Mul(params.BeaconConfig().SecondsPerSlot))
	c := cache.New(secsInEpoch*time.Second, 2*secsInEpoch*time.Second)
	pool := &AttCaches{
		unAggregatedAtt: make(map[[32]byte]*ethpb.Attestation),
		aggregatedAtt:   make(map[[32]byte][]*ethpb.Attestation),
		forkchoiceAtt:   make(map[[32]byte]*ethpb.Attestation),
		blockAtt:        make(map[[32]byte][]*ethpb.Attestation),
		seenAtt:         c,
	}

	return pool
}
