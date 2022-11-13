// Package slasher defines a gRPC server implementation of a slasher service
// which allows for checking if attestations or blocks are slashable.
// slasher定义了一个gRPC server的实现，对于一个slasher service，允许检查是否
// attestations或者blocks是slashable
package slasher

import (
	slasherservice "github.com/prysmaticlabs/prysm/beacon-chain/slasher"
)

// Server defines a server implementation of the gRPC slasher service.
// Server定义了一个server实现，对于gRPC slasher service
type Server struct {
	SlashingChecker slasherservice.SlashingChecker
}
