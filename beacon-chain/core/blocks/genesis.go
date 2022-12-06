// Package blocks contains block processing libraries according to
// the Ethereum beacon chain spec.
// blocks包含了处理block的库，根据Ethereum beacon chain spec
package blocks

import (
	fieldparams "github.com/prysmaticlabs/prysm/v3/config/fieldparams"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
)

// NewGenesisBlock returns the canonical, genesis block for the beacon chain protocol.
// NewGenesisBlock为beacon chain protocol返回canonical, genesis block
func NewGenesisBlock(stateRoot []byte) *ethpb.SignedBeaconBlock {
	zeroHash := params.BeaconConfig().ZeroHash[:]
	block := &ethpb.SignedBeaconBlock{
		Block: &ethpb.BeaconBlock{
			ParentRoot: zeroHash,
			StateRoot:  bytesutil.PadTo(stateRoot, 32),
			Body: &ethpb.BeaconBlockBody{
				RandaoReveal: make([]byte, fieldparams.BLSSignatureLength),
				Eth1Data: &ethpb.Eth1Data{
					DepositRoot: make([]byte, 32),
					BlockHash:   make([]byte, 32),
				},
				Graffiti: make([]byte, 32),
			},
		},
		Signature: params.BeaconConfig().EmptySignature[:],
	}
	return block
}
