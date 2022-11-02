package block

import "github.com/prysmaticlabs/prysm/async/event"

// Notifier interface defines the methods of the service that provides block updates to consumers.
// 提供block updates给consumer
type Notifier interface {
	BlockFeed() *event.Feed
}
