package sync

import (
	"context"
	"errors"
	"fmt"

	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"google.golang.org/protobuf/proto"
)

// syncContributionAndProofSubscriber forwards the incoming validated sync contributions and proof to the
// contribution pool for processing.
// syncContributionAndProofSubscriber转发收到的validated sync contributions以及proof到contribution pool
// 用于处理
// skipcq: SCC-U1000
func (s *Service) syncContributionAndProofSubscriber(_ context.Context, msg proto.Message) error {
	sContr, ok := msg.(*ethpb.SignedContributionAndProof)
	if !ok {
		return fmt.Errorf("message was not type *eth.SignedAggregateAttestationAndProof, type=%T", msg)
	}

	if sContr.Message == nil || sContr.Message.Contribution == nil {
		return errors.New("nil contribution")
	}

	return s.cfg.syncCommsPool.SaveSyncCommitteeContribution(sContr.Message.Contribution)
}
