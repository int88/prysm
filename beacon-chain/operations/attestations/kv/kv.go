// Package kv includes a key-value store implementation
// of an attestation cache used to satisfy important use-cases
// such as aggregation in a beacon node runtime.
// kv包包含了一个键值存储的实现，对于一个attestation cache，用于满足重要的使用场景
// 例如在beacon node runtime中的aggregation
package kv

import (
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/crypto/hash"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
)

var hashFn = hash.HashProto

// AttCaches defines the caches used to satisfy attestation pool interface.
// These caches are KV store for various attestations
// such are unaggregated, aggregated or attestations within a block.
// AttCaches定义了缓存用于满足attestation pool接口，这些caches是键值对，用于各种attestations
// 例如aggregated, unaggregated或者一个block内的attestations
type AttCaches struct {
	aggregatedAttLock sync.RWMutex
	// aggregated attestations
	aggregatedAtt      map[[32]byte][]*ethpb.Attestation
	unAggregateAttLock sync.RWMutex
	// unaggregated attestations
	unAggregatedAtt   map[[32]byte]*ethpb.Attestation
	forkchoiceAttLock sync.RWMutex
	// forkchoice attestations
	// forkchoice的attestations
	forkchoiceAtt map[[32]byte]*ethpb.Attestation
	blockAttLock  sync.RWMutex
	blockAtt      map[[32]byte][]*ethpb.Attestation
	seenAtt       *cache.Cache
}

// NewAttCaches initializes a new attestation pool consists of multiple KV store in cache for
// various kind of attestations.
// NewAttCaches初始化一个新的attestation pool，缓存中包含多个KV store，用于各种的attestations
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
