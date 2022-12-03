package state

import "github.com/prysmaticlabs/prysm/v3/async/event"

// Notifier interface defines the methods of the service that provides state updates to consumers.
// Notifier接口定义了服务的方法，用于提供state updates到consumers
type Notifier interface {
	StateFeed() *event.Feed
}
