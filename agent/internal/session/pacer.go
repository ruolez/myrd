package session

import (
	"errors"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

const (
	pacingFactor  = 2.5                   // send rate over the GCC target, as libwebrtc
	pacingBurst   = 5 * time.Millisecond  // sent without waiting after a quiet spell
	maxQueueDelay = 50 * time.Millisecond // the rate rises so nothing waits longer
	minBurstBytes = 2 * rtpMTU
)

var errPacerClosed = errors.New("pacer closed")

type pacedPacket struct {
	header  rtp.Header
	payload []byte
	attrs   interceptor.Attributes
	at      time.Time // queued
	w       interceptor.RTPWriter
}

// framePacer is the gcc.Pacer. It spreads a large frame (a keyframe, a
// scroll) over a few milliseconds instead of handing the network a
// line-rate burst, which shallow Wi-Fi and TURN buffers drop. pion's leaky
// bucket paces at 1.5x the target and, after an idle period, lets a whole
// keyframe through in one burst.
type framePacer struct {
	now func() time.Time

	mu      sync.Mutex
	rate    float64 // bytes per second at the target bitrate
	tokens  float64
	last    time.Time
	queue   []pacedPacket
	queued  int
	writers map[uint32]interceptor.RTPWriter
	closed  bool
	wake    chan struct{}
	done    chan struct{}
}

func newFramePacer(initialBps int) *framePacer {
	p := &framePacer{now: time.Now, writers: map[uint32]interceptor.RTPWriter{},
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	p.SetTargetBitrate(initialBps)
	p.last = p.now()
	go p.run()
	return p
}

func (p *framePacer) AddStream(ssrc uint32, w interceptor.RTPWriter) {
	p.mu.Lock()
	p.writers[ssrc] = w
	p.mu.Unlock()
}

func (p *framePacer) SetTargetBitrate(bps int) {
	p.mu.Lock()
	p.rate = pacingFactor * float64(bps) / 8
	p.mu.Unlock()
}

func (p *framePacer) Write(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
	pk := pacedPacket{header: h.Clone(), payload: append([]byte(nil), payload...), attrs: a, at: p.now()}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, errPacerClosed
	}
	p.queue = append(p.queue, pk)
	p.queued += len(payload)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return h.MarshalSize() + len(payload), nil
}

func (p *framePacer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	return nil
}

// sendRate is the pacing rate with this much queued and left until the
// oldest packet's maxQueueDelay runs out: at least the paced rate, more when
// the queue would not drain in time. Keyed to the oldest packet (as libwebrtc
// does), every packet leaves before its own deadline.
func sendRate(rate float64, queued int, left time.Duration) float64 {
	return max(rate, float64(queued)/max(left, time.Millisecond).Seconds())
}

// refill adds the tokens earned since last, capped at one burst.
func refill(tokens, rate float64, elapsed time.Duration) float64 {
	return min(tokens+rate*elapsed.Seconds(), max(rate*pacingBurst.Seconds(), minBurstBytes))
}

func (p *framePacer) run() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.mu.Unlock()
			select {
			case <-p.wake:
				continue
			case <-p.done:
				return
			}
		}
		now := p.now()
		rate := sendRate(p.rate, p.queued, p.queue[0].at.Add(maxQueueDelay).Sub(now))
		p.tokens = refill(p.tokens, rate, now.Sub(p.last))
		p.last = now
		if p.tokens <= 0 {
			wait := time.Duration(-p.tokens / rate * float64(time.Second))
			p.mu.Unlock()
			select {
			case <-time.After(max(wait, time.Millisecond)):
			case <-p.done:
				return
			}
			continue
		}
		var batch []pacedPacket
		for len(p.queue) > 0 && p.tokens > 0 {
			pk := p.queue[0]
			p.queue[0] = pacedPacket{}
			p.queue = p.queue[1:]
			p.queued -= len(pk.payload)
			p.tokens -= float64(len(pk.payload))
			pk.w = p.writers[pk.header.SSRC]
			batch = append(batch, pk)
		}
		p.mu.Unlock()
		for _, pk := range batch {
			if pk.w != nil {
				_, _ = pk.w.Write(&pk.header, pk.payload, pk.attrs)
			}
		}
	}
}
