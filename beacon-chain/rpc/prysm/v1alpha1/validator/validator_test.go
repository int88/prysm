package validator

import (
	"io"
	"testing"

	"github.com/prysmaticlabs/prysm/config/params"
	"github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetOutput(io.Discard)
	// Use minimal config to reduce test setup time.
	// 使用最小的配置来降低test的setup time
	prevConfig := params.BeaconConfig().Copy()
	defer params.OverrideBeaconConfig(prevConfig)
	params.OverrideBeaconConfig(params.MinimalSpecConfig())

	m.Run()
}
