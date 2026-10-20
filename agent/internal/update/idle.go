package update

import (
	"context"
	"errors"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// forceGrace is how many idle polls a forced update gives the ended
// sessions to close before installing anyway.
const forceGrace = 3

var errSuperseded = errors.New("superseded by a newer update check")

// waitIdle returns nil once busy() reports no session. A closed force ends
// the sessions (reason agent_update) instead of waiting; a value on
// supersede abandons an unforced wait. tick paces the polls.
func waitIdle(ctx context.Context, busy func() bool, force <-chan struct{}, supersede <-chan struct{},
	endAll func(string), tick <-chan time.Time) error {
	forced := -1 // polls since the sessions were ended
	forceNow := func() {
		force = nil
		endAll(proto.EndAgentUpdate)
		forced = 0
	}
	isForced := func() bool {
		select {
		case <-force:
			forceNow()
			return true
		default:
			return false
		}
	}
	for {
		if !busy() {
			return nil
		}
		if isForced() {
			continue
		}
		if forced >= forceGrace {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-force:
			forceNow()
		case <-supersede:
			if !isForced() && forced < 0 {
				return errSuperseded
			}
		case <-tick:
			if forced >= 0 {
				forced++
			}
		}
	}
}
