// Package voluntaryexits defines an in-memory pool of received
// voluntary exit events by the beacon node, handling their lifecycle
// and performing integrity checks before serving them as objects
// for validators to include in blocks.
// voluntaryexits定义了一个内存中的pool，用于存放beacon node接收到的voluntary
// exit，处理它们的生命周期以及执行完整性检查，在服务它们，作为objects for validators
// 在加入到blocks中
package voluntaryexits
