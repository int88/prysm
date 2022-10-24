package slots

import (
	"context"
	"fmt"
	"time"

	"github.com/prysmaticlabs/prysm/config/params"
	prysmTime "github.com/prysmaticlabs/prysm/time"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("prefix", "slotutil")

// CountdownToGenesis starts a ticker at the specified duration
// CountdownToGenesis启动一个ticker，在特定的时间间隔
// logging the remaining minutes until the genesis chainstart event
// along with important genesis state metadata such as number
// of genesis validators.
// 记录剩余的分钟，直到genesis chainstart event，以及重要的genesis state metadata，例如
// genesis validators的数目
func CountdownToGenesis(ctx context.Context, genesisTime time.Time, genesisValidatorCount uint64, genesisStateRoot [32]byte) {
	ticker := time.NewTicker(params.BeaconConfig().GenesisCountdownInterval)
	defer func() {
		// Used in anonymous function to make sure that updated (per second) ticker is stopped.
		ticker.Stop()
	}()
	logFields := logrus.Fields{
		"genesisValidators": fmt.Sprintf("%d", genesisValidatorCount),
		"genesisTime":       fmt.Sprintf("%v", genesisTime),
		"genesisStateRoot":  fmt.Sprintf("%x", genesisStateRoot),
	}
	secondTimerActivated := false
	for {
		currentTime := prysmTime.Now()
		if currentTime.After(genesisTime) {
			log.WithFields(logFields).Info("Chain genesis time reached")
			return
		}
		timeRemaining := genesisTime.Sub(currentTime)
		if !secondTimerActivated && timeRemaining <= 2*time.Minute {
			ticker.Stop()
			// Replace ticker with a one having higher granularity.
			ticker = time.NewTicker(time.Second)
			secondTimerActivated = true
		}
		if timeRemaining >= time.Second {
			log.WithFields(logFields).Infof(
				"%s until chain genesis",
				timeRemaining.Truncate(time.Second),
			)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			log.Debug("Context closed, exiting routine")
			return
		}
	}
}
