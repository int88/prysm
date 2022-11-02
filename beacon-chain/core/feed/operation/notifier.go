package operation

import "github.com/prysmaticlabs/prysm/async/event"

// Notifier interface defines the methods of the service that provides beacon block operation updates to consumers.
// Notifier接口定义了service的方法，提供beacon block operation updates给consumer
type Notifier interface {
	OperationFeed() *event.Feed
}
