package sysinfo

import (
	"context"
	"time"

	"github.com/ruolez/remotectl/proto"
)

const (
	pollEvery   = 5 * time.Second  // how often the user and lock state are checked
	reportEvery = 60 * time.Second // a full report at least this often
	changeGap   = 10 * time.Second // earliest report after the previous one when the user or lock changes
)

// due reports whether a fresh reading should be sent now.
func due(last *proto.HostInfo, cur proto.HostInfo, sinceSent time.Duration, kicked bool) bool {
	if kicked || last == nil || sinceSent >= reportEvery {
		return true
	}
	return sinceSent >= changeGap && (!eqStr(last.User, cur.User) || !eqBool(last.Locked, cur.Locked))
}

func eqStr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func eqBool(a, b *bool) bool  { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// Reporter sends device.info: right after Kick (a new connection's
// hello_ack), then every minute, and soon after the console user or the
// lock state changes.
type Reporter struct {
	Collect func() proto.HostInfo
	Send    func(proto.HostInfo) error
	// Enabled is false while the server does not accept device.info.
	Enabled func() bool

	kick chan struct{}
}

func NewReporter(collect func() proto.HostInfo, send func(proto.HostInfo) error, enabled func() bool) *Reporter {
	return &Reporter{Collect: collect, Send: send, Enabled: enabled, kick: make(chan struct{}, 1)}
}

// Kick asks for a report as soon as possible.
func (r *Reporter) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *Reporter) Run(ctx context.Context) {
	r.Collect() // a first CPU reading, so the first report has a CPU figure
	start := time.Now()
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	var last *proto.HostInfo
	var sent time.Time
	for {
		kicked := false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
			kicked = true
		}
		if !r.Enabled() {
			continue
		}
		if wait := minCPUWindow - time.Since(start); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		cur := r.Collect()
		if !due(last, cur, time.Since(sent), kicked) {
			continue
		}
		if r.Send(cur) == nil {
			last, sent = &cur, time.Now()
		}
	}
}
