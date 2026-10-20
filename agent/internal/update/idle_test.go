package update

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

func TestWaitIdle(t *testing.T) {
	type result struct {
		Err       error
		Ended     []string
		BusyCalls int
	}
	cases := []struct {
		name      string
		busy      []bool // answers in order; the last one repeats
		ticks     int
		force     bool
		supersede bool
		cancelled bool
		want      result
	}{
		{"idle at once", []bool{false}, 0, false, false, false, result{nil, nil, 1}},
		{"waits for the session to end", []bool{true, true, false}, 5, false, false, false, result{nil, nil, 3}},
		{"force ends sessions and proceeds once idle", []bool{true, true, false}, 5, true, false, false,
			result{nil, []string{proto.EndAgentUpdate}, 3}},
		{"force proceeds after the grace even if busy", []bool{true}, 10, true, false, false,
			result{nil, []string{proto.EndAgentUpdate}, 2 + forceGrace}},
		{"newer trigger supersedes the wait", []bool{true}, 0, false, true, false, result{errSuperseded, nil, 1}},
		{"force wins over supersede", []bool{true, false}, 0, true, true, false,
			result{nil, []string{proto.EndAgentUpdate}, 2}},
		{"cancelled", []bool{true}, 0, false, false, true, result{context.Canceled, nil, 1}},
	}
	for _, c := range cases {
		var r result
		busy := func() bool {
			i := min(r.BusyCalls, len(c.busy)-1)
			r.BusyCalls++
			return c.busy[i]
		}
		ctx, cancel := context.WithCancel(context.Background())
		if c.cancelled {
			cancel()
		}
		force, supersede := make(chan struct{}), make(chan struct{}, 1)
		if c.force {
			close(force)
		}
		if c.supersede {
			supersede <- struct{}{}
		}
		tick := make(chan time.Time, c.ticks)
		for range c.ticks {
			tick <- t0
		}
		r.Err = waitIdle(ctx, busy, force, supersede, func(reason string) { r.Ended = append(r.Ended, reason) }, tick)
		cancel()
		if !errors.Is(r.Err, c.want.Err) || !reflect.DeepEqual(r.Ended, c.want.Ended) || r.BusyCalls != c.want.BusyCalls {
			t.Errorf("%s: got %+v, want %+v", c.name, r, c.want)
		}
	}
}
