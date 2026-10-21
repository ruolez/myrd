package gcc

import (
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

const (
	// 20 gaps of these span 1/128 s and 1/64 s, so the rates come out exact.
	gap128 = 390_625 * time.Nanosecond
	gap64  = 781_250 * time.Nanosecond

	probePacketSize = 1000
	rate128         = 20 * probePacketSize * 8 * 128 // 20 packets' bits over 1/128 s
	rate64          = 20 * probePacketSize * 8 * 64
)

var probeEpoch = time.Unix(1000, 0)

// probeAcks is n acks sent and received evenly from start.
func probeAcks(start time.Duration, n int, sendGap, recvGap time.Duration) []Acknowledgment {
	acks := make([]Acknowledgment, n)
	for i := range acks {
		acks[i] = Acknowledgment{
			SequenceNumber: uint16(i),
			Size:           probePacketSize,
			Departure:      probeEpoch.Add(start + time.Duration(i)*sendGap),
			Arrival:        probeEpoch.Add(time.Second + start + time.Duration(i)*recvGap),
		}
	}
	return acks
}

func withAck(acks []Acknowledgment, i int, f func(*Acknowledgment)) []Acknowledgment {
	f(&acks[i])
	return acks
}

// bridged is 21 acks received gap64 apart whose departures are 100 µs apart
// except for one bridge gap in the middle.
func bridged(bridge time.Duration) []Acknowledgment {
	acks := probeAcks(0, 21, 0, gap64)
	dep := probeEpoch
	for i := range acks {
		if i == 10 {
			dep = dep.Add(bridge)
		} else if i > 0 {
			dep = dep.Add(100 * time.Microsecond)
		}
		acks[i].Departure = dep
	}
	return acks
}

func TestProbeRate(t *testing.T) {
	cases := []struct {
		name string
		acks []Acknowledgment
		want int
	}{
		{"no feedback", nil, 0},
		{"one burst", probeAcks(0, 21, gap128, gap128), rate128},
		{"receive-limited", probeAcks(0, 21, gap128, gap64), rate64},
		{"send-limited", probeAcks(0, 21, gap64, gap128), rate64},
		{"first packet's size is not counted",
			withAck(probeAcks(0, 21, gap128, gap128), 0, func(a *Acknowledgment) { a.Size = 9000 }), rate128},
		{"too few packets", probeAcks(0, 19, gap64, gap64), 0},
		{"send span too short", probeAcks(0, 21, 90*time.Microsecond, gap64), 0},
		{"receive span too short", probeAcks(0, 21, gap64, 90*time.Microsecond), 0},
		{"a lost packet disqualifies",
			withAck(probeAcks(0, 21, gap128, gap128), 7, func(a *Acknowledgment) { a.Arrival = time.Time{} }), 0},
		{"a packet unknown to the history disqualifies",
			withAck(probeAcks(0, 22, gap128, gap128), 7, func(a *Acknowledgment) { *a = Acknowledgment{} }), 0},
		{"max over bursts",
			append(probeAcks(0, 21, gap64, gap64), probeAcks(100*time.Millisecond, 21, gap128, gap128)...), rate128},
		{"max over bursts, faster first",
			append(probeAcks(0, 21, gap128, gap128), probeAcks(100*time.Millisecond, 21, gap64, gap64)...), rate128},
		{"a disqualified burst does not spoil the next",
			append(withAck(probeAcks(0, 21, gap128, gap128), 3, func(a *Acknowledgment) { a.Arrival = time.Time{} }),
				probeAcks(100*time.Millisecond, 21, gap64, gap64)...), rate64},
		{"a 4 ms gap stays in the burst", bridged(probeBurstGap), rate64},
		{"a longer gap splits it", bridged(probeBurstGap + time.Nanosecond), 0},
		{"departure going back splits it",
			append(probeAcks(10*time.Millisecond, 10, gap128, gap128), probeAcks(0, 11, gap128, gap128)...), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEqual(t, tc.want, probeRate(tc.acks))
		})
	}
}

// TestSendSideBWEProbe feeds one TWCC feedback for a tight 40-packet burst
// (sent over 2.9 ms, received over 9.75 ms, about 38 Mbps) to a 1 Mbps
// estimator.
func TestSendSideBWEProbe(t *testing.T) {
	const (
		packets = 40
		initial = 1_000_000
	)
	cases := []struct {
		name       string
		usage      usage
		want       int
		wantChange bool
	}{
		{"raises the target 3x", usageNormal, 3 * initial, true},
		{"not while overusing", usageOver, initial, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bwe, err := NewSendSideBWE(SendSideBWEInitialBitrate(initial), SendSideBWEMinBitrate(100_000),
				SendSideBWEMaxBitrate(50_000_000), SendSideBWEPacer(newTestPacer(0)))
			requireNoError(t, err)
			defer func() { assertNoError(t, bwe.Close()) }()
			changes := make(chan int, 1)
			bwe.OnTargetBitrateChange(func(b int) { changes <- b })
			bwe.delayController.onDelayStats(DelayStats{Usage: tc.usage})

			attrs := interceptor.Attributes{}
			attrs.Set(TwccExtensionAttributesKey, uint8(1))
			sent := probeEpoch.Add(-50 * time.Millisecond)
			for i := range packets {
				ext, err := (&rtp.TransportCCExtension{TransportSequence: uint16(65_520 + i)}).Marshal()
				requireNoError(t, err)
				h := &rtp.Header{SSRC: 1, SequenceNumber: uint16(i)}
				requireNoError(t, h.SetExtension(1, ext))
				requireNoError(t, bwe.feedbackAdapter.OnSent(sent.Add(time.Duration(i)*75*time.Microsecond), h, 1200, attrs))
			}
			deltas := make([]*rtcp.RecvDelta, packets)
			for i := range deltas {
				deltas[i] = &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250}
			}
			fb := &rtcp.TransportLayerCC{
				BaseSequenceNumber: 65_520,
				PacketStatusCount:  packets,
				ReferenceTime:      1,
				PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{
					PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: packets,
				}},
				RecvDeltas: deltas,
			}
			bwe.now = func() time.Time { return probeEpoch }
			requireNoError(t, bwe.WriteRTCP([]rtcp.Packet{fb}, nil))

			assertEqual(t, tc.want, bwe.GetTargetBitrate())
			st := bwe.GetStats()
			assertEqual(t, 0.0, st["averageLoss"])
			// first packet: sent 50 ms before the feedback, 9.75 ms of it spent at the receiver
			assertEqual(t, 40.25, st["rtt"])
			if tc.wantChange {
				assertEqual(t, tc.want, st["delayTargetBitrate"])
				assertEqual(t, tc.want, st["lossTargetBitrate"])
				select {
				case b := <-changes:
					assertEqual(t, tc.want, b)
				case <-time.After(time.Second):
					t.Error("OnTargetBitrateChange not called")
				}
			} else {
				select {
				case b := <-changes:
					t.Errorf("OnTargetBitrateChange(%d)", b)
				case <-time.After(20 * time.Millisecond):
				}
			}
		})
	}
}
