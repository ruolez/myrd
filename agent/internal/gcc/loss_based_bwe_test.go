package gcc

import (
	"testing"
	"time"

	"github.com/pion/logging"
)

// feed sends seconds of feedback, a report of perReport packets every
// 50 ms, losing every lossEvery-th packet (0: none).
func feed(e *lossBasedBandwidthEstimator, now *time.Time, seconds, perReport, lossEvery int) {
	n := 0
	for i := 0; i < seconds*20; i++ {
		acks := make([]Acknowledgment, perReport)
		for j := range acks {
			n++
			if lossEvery == 0 || n%lossEvery != 0 {
				acks[j].Arrival = *now
			}
		}
		*now = now.Add(50 * time.Millisecond)
		e.updateLossEstimate(acks)
	}
}

func newTestLossBWE(now *time.Time) *lossBasedBandwidthEstimator {
	e := newLossBasedBWE(5_000_000, 1_000_000, 50_000_000, logging.NewDefaultLoggerFactory())
	e.now = func() time.Time { return *now }
	return e
}

func TestLossBasedBWEHoldsUnderSteadyModerateLoss(t *testing.T) {
	for _, perReport := range []int{3, 12, 60} { // a static screen, a few frames, a busy one
		now := time.Unix(1000, 0)
		e := newTestLossBWE(&now)
		feed(e, &now, 30, perReport, 20) // 5%
		if e.bitrate != 5_000_000 {
			t.Errorf("%d packets per report: bitrate %d after 30 s of 5%% loss, want 5000000 held", perReport, e.bitrate)
		}
		if e.averageLoss < 0.04 || e.averageLoss > 0.06 {
			t.Errorf("%d packets per report: loss %v, want about 0.05", perReport, e.averageLoss)
		}
	}
}

func TestLossBasedBWEDecreasesOnHeavyLossButNotBelowTheFloor(t *testing.T) {
	now := time.Unix(1000, 0)
	e := newTestLossBWE(&now)
	feed(e, &now, 2, 12, 4) // 25%
	if e.bitrate >= 5_000_000 {
		t.Fatalf("bitrate %d after 25%% loss, want a decrease", e.bitrate)
	}
	feed(e, &now, 60, 12, 4)
	if e.bitrate != 1_000_000 {
		t.Fatalf("bitrate %d after a minute of 25%% loss, want the 1 Mbps floor", e.bitrate)
	}
}

func TestLossBasedBWEIncreasesWithoutLoss(t *testing.T) {
	now := time.Unix(1000, 0)
	e := newTestLossBWE(&now)
	feed(e, &now, 3, 12, 0)
	if e.bitrate <= 5_000_000 {
		t.Fatalf("bitrate %d after 3 s without loss, want an increase", e.bitrate)
	}
}

func TestLossBasedBWEWaitsForEnoughPackets(t *testing.T) {
	now := time.Unix(1000, 0)
	e := newTestLossBWE(&now)
	e.updateLossEstimate([]Acknowledgment{{}, {}, {Arrival: now}}) // two of three lost
	now = now.Add(2 * time.Second)
	e.updateLossEstimate([]Acknowledgment{{Arrival: now}})
	if e.bitrate != 5_000_000 || e.averageLoss != 0 {
		t.Fatalf("bitrate %d loss %v from 4 packets, want no decision", e.bitrate, e.averageLoss)
	}
}
