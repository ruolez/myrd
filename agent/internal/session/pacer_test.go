package session

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestSendRateDrainsTheQueueBeforeTheOldestDeadline(t *testing.T) {
	const rate = 1_000_000 // bytes/s
	cases := []struct {
		queued int
		left   time.Duration
		want   float64
	}{
		{0, maxQueueDelay, rate},
		{rate / 20, 50 * time.Millisecond, rate},       // exactly 50 ms at the paced rate
		{rate / 5, 50 * time.Millisecond, 4 * rate},    // would take 200 ms: drain in 50
		{rate / 5, 10 * time.Millisecond, 20 * rate},   // the oldest has 10 ms left
		{rate / 100, -5 * time.Millisecond, 10 * rate}, // overdue: as fast as 1 ms allows
	}
	for _, c := range cases {
		if got := sendRate(rate, c.queued, c.left); !near(got, c.want) {
			t.Errorf("sendRate(%d queued, %v left) = %v, want %v", c.queued, c.left, got, c.want)
		}
	}
}

func TestRefillCapsTheBurstAfterAQuietSpell(t *testing.T) {
	const rate = 1_000_000
	cases := []struct {
		name    string
		tokens  float64
		rate    float64
		elapsed time.Duration
		want    float64
	}{
		{"earns at the rate", -3000, rate, 2 * time.Millisecond, -1000},
		{"a long idle earns one burst", 0, rate, time.Minute, rate * 0.005},
		{"slow links still pass two packets", 0, 10_000, time.Minute, minBurstBytes},
	}
	for _, c := range cases {
		if got := refill(c.tokens, c.rate, c.elapsed); !near(got, c.want) {
			t.Errorf("%s: refill = %v, want %v", c.name, got, c.want)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b)) }

type timedSink struct {
	mu   sync.Mutex
	seqs []uint16
	last time.Time
}

func (s *timedSink) Write(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
	s.mu.Lock()
	s.seqs = append(s.seqs, h.SequenceNumber)
	s.last = time.Now()
	s.mu.Unlock()
	return len(payload), nil
}

func (s *timedSink) done(n int) (bool, time.Time, []uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seqs) == n, s.last, append([]uint16(nil), s.seqs...)
}

// pace writes n packets of size bytes at once and returns how long the
// pacer took to send them all, checking they kept their order.
func pace(t *testing.T, targetBps, n, size int) time.Duration {
	t.Helper()
	p := newFramePacer(targetBps)
	defer p.Close()
	sink := &timedSink{}
	p.AddStream(1, sink)
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := p.Write(&rtp.Header{SSRC: 1, SequenceNumber: uint16(i)}, make([]byte, size), nil); err != nil {
			t.Fatal(err)
		}
	}
	var last time.Time
	var seqs []uint16
	waitFor(t, "paced packets", func() bool {
		var ok bool
		ok, last, seqs = sink.done(n)
		return ok
	})
	for i, s := range seqs {
		if s != uint16(i) {
			t.Fatalf("packet %d sent as #%d: order changed", s, i)
		}
	}
	return last.Sub(start)
}

func TestPacerSpreadsAFrameAtTheBoostedTargetRate(t *testing.T) {
	// 100 KB at 8 Mbps x 2.5 = 2.5 MB/s takes 40 ms; a line-rate burst takes ~0.
	took := pace(t, 8_000_000, 100, 1000)
	if took < 25*time.Millisecond || took > 150*time.Millisecond {
		t.Fatalf("100 KB took %v, want about 40 ms", took)
	}
}

func TestPacerNeverHoldsAKeyframePastMaxQueueDelay(t *testing.T) {
	// 1 MB at 1 Mbps would take 3.2 s at the paced rate.
	took := pace(t, 1_000_000, 1000, 1000)
	if took > maxQueueDelay+100*time.Millisecond {
		t.Fatalf("1 MB took %v, want at most about %v", took, maxQueueDelay)
	}
}

func TestPacerRejectsWritesAfterClose(t *testing.T) {
	p := newFramePacer(1_000_000)
	_ = p.Close()
	if _, err := p.Write(&rtp.Header{}, []byte{1}, nil); err != errPacerClosed {
		t.Fatalf("write after close: err = %v, want errPacerClosed", err)
	}
}
