package transition

import (
	"bytes"
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
)

type nextSlotCache struct {
	sync.RWMutex
	root  []byte
	state state.BeaconState
}

var (
	nsc nextSlotCache
	// Metrics for the validator cache.
	nextSlotCacheHit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "next_slot_cache_hit",
		Help: "The total number of cache hits on the next slot state cache.",
	})
	nextSlotCacheMiss = promauto.NewCounter(prometheus.CounterOpts{
		Name: "next_slot_cache_miss",
		Help: "The total number of cache misses on the next slot state cache.",
	})
)

// NextSlotState returns the saved state if the input root matches the root in `nextSlotCache`. Returns nil otherwise.
// This is useful to check before processing slots. With a cache hit, it will return last processed state with slot plus
// one advancement.
// NextSlotState返回保存的state，如果输入的root匹配 `nextSlotCache`中的root，否则返回nil，这在处理slots之前检查是有用的
// 它会返回最新的processed state，slot向前移动一格
func NextSlotState(_ context.Context, root []byte) (state.BeaconState, error) {
	nsc.RLock()
	defer nsc.RUnlock()
	if !bytes.Equal(root, nsc.root) || bytes.Equal(root, []byte{}) {
		nextSlotCacheMiss.Inc()
		return nil, nil
	}
	nextSlotCacheHit.Inc()
	// Returning copied state.
	// 返回拷贝的state
	return nsc.state.Copy(), nil
}

// UpdateNextSlotCache updates the `nextSlotCache`. It saves the input state after advancing the state slot by 1
// by calling `ProcessSlots`, it also saves the input root for later look up.
// This is useful to call after successfully processing a block.
// UpdateNextSlotCache更新`nextSlotCache`，它保存输入的state，在将state slot向前移动1之后，通过调用`ProcessSlots`
// 它同时保存输入的root，用于后续的查询，在成功处理一个block之后调用是有用的
func UpdateNextSlotCache(ctx context.Context, root []byte, state state.BeaconState) error {
	// Advancing one slot by using a copied state.
	// 移动一个slot，通过使用copied state
	copied := state.Copy()
	copied, err := ProcessSlots(ctx, copied, copied.Slot()+1)
	if err != nil {
		return err
	}

	nsc.Lock()
	defer nsc.Unlock()

	// 写入nsc
	nsc.root = root
	nsc.state = copied
	return nil
}
