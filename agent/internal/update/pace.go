package update

import (
	"context"
	"sync"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// StatusEvery is the protocol's limit on update.status messages.
const StatusEvery = 2 * time.Second

// pacer sends at most one status per interval. A status arriving sooner
// waits and replaces any status still waiting, so the latest state is always
// sent and only intermediate ones are dropped. A negative interval sends
// everything at once.
type pacer struct {
	every time.Duration
	send  func(proto.UpdateStatus)

	mu      sync.Mutex
	last    time.Time
	pending *proto.UpdateStatus
	idle    chan struct{} // closed while nothing is pending
}

func newPacer(every time.Duration, send func(proto.UpdateStatus)) *pacer {
	idle := make(chan struct{})
	close(idle)
	return &pacer{every: every, send: send, idle: idle}
}

func (p *pacer) push(s proto.UpdateStatus) {
	if p.every < 0 {
		p.send(s)
		return
	}
	p.mu.Lock()
	if p.pending != nil {
		p.pending = &s
		p.mu.Unlock()
		return
	}
	wait := p.every - time.Since(p.last)
	if wait <= 0 {
		p.last = time.Now()
		p.mu.Unlock()
		p.send(s)
		return
	}
	p.pending = &s
	p.idle = make(chan struct{})
	p.mu.Unlock()
	time.AfterFunc(wait, p.fire)
}

func (p *pacer) fire() {
	p.mu.Lock()
	s := *p.pending
	p.pending = nil
	p.last = time.Now()
	idle := p.idle
	p.mu.Unlock()
	p.send(s)
	close(idle)
}

// flush waits until the waiting status, if any, has been sent.
func (p *pacer) flush(ctx context.Context) {
	p.mu.Lock()
	idle := p.idle
	p.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
	}
}
