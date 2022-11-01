package synccommittee

import (
	"sync"

	"github.com/prysmaticlabs/prysm/container/queue"
)

// Store defines the caches for various sync committee objects
// such as message(un-aggregated) and contribution(aggregated).
// Store定义了缓存用于各种sync committee对象，例如message（un-aggregated）以及contribution（aggregated）
type Store struct {
	messageLock       sync.RWMutex
	messageCache      *queue.PriorityQueue
	contributionLock  sync.RWMutex
	contributionCache *queue.PriorityQueue
}

// NewStore initializes a new sync committee store.
// NewStore初始化一个新的sync committee store
func NewStore() *Store {
	return &Store{
		messageCache:      queue.New(),
		contributionCache: queue.New(),
	}
}
