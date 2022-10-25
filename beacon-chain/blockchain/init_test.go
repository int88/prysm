package blockchain

import (
	"github.com/prysmaticlabs/prysm/config/params"
)

func init() {
	// Override network name so that hardcoded genesis files are not loaded.
	// 覆盖network name，这样硬编码的genesis files不会被加载
	if err := params.SetActive(params.MainnetTestConfig()); err != nil {
		panic(err)
	}
}
