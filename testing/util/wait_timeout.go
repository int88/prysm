package util

import (
	"sync"
	"time"
)

// WaitTimeout will wait for a WaitGroup to resolve within a timeout interval.
// Returns true if the waitgroup exceeded the timeout.
// WaitTimeout会等待一个WaitGroup在一个timeout interval内解析，返回true，如果waitgroup
// 超过了timeout
func WaitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		wg.Wait()
	}()
	select {
	case <-ch:
		return false
	case <-time.After(timeout):
		return true
	}
}
