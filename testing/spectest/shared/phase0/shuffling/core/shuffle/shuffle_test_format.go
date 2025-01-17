package shuffle

import types "github.com/prysmaticlabs/prysm/consensus-types/primitives"

// ShuffleTestCase --
type ShuffleTestCase struct {
	Seed    string                 `yaml:"seed"`
	Count   uint64                 `yaml:"count"`
	Mapping []types.ValidatorIndex `yaml:"mapping"`
}
