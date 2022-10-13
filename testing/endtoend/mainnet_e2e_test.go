package endtoend

import (
	"testing"
)

// Run mainnet e2e config with the current release validator against latest beacon node.
// 运行mainnet的e2e配置，用当前release的validator，对于最新的beacon节点
func TestEndToEnd_MainnetConfig_ValidatorAtCurrentRelease(t *testing.T) {
	e2eMainnet(t, true, false).run()
}
