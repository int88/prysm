// Package slashings defines an in-memory pool of received
// slashing events by the beacon node, handling their lifecycle
// and performing integrity checks before serving them as objects
// for validators to include in blocks.
// slashings定义了一个内存中的pool用于接收slashing events，通过beacon node
// 处理他们的生命周期并且执行integrity checks，在服务他们作为validators的objects
// 用于包含在blocks
package slashings
