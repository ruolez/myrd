package gcc

import "time"

const (
	probeBurstGap    = 4 * time.Millisecond // a longer departure gap starts a new burst
	probeMinPackets  = 20
	probeMinInterval = 2 * time.Millisecond // for both the send and the receive span
)

type probeBurst struct {
	packets, bytes            int
	lost                      bool // or unknown to the feedback history
	firstSent, lastSent       time.Time
	firstArrival, lastArrival time.Time
	firstSentSize             int
	firstArrivalSize          int
}

// probeRate is the highest rate (bps) a burst in one feedback got through
// at, 0 when none qualifies. The pacer sends every frame as a tight burst, so
// a large frame is a probe as in libwebrtc's ProbeBitrateEstimator. acks are
// in transport sequence order, which is departure order; a departure gap over
// probeBurstGap (or a step back) splits bursts. A burst counts with at least
// probeMinPackets, none lost or unknown, and send and receive spans of at
// least probeMinInterval; its rate is min(send, receive), each the bits of
// all packets but the first divided by the span.
func probeRate(acks []Acknowledgment) int {
	best := 0
	var b probeBurst
	for _, a := range acks {
		if a.Departure.IsZero() {
			b.lost = true
			continue
		}
		if b.packets > 0 && (a.Departure.Sub(b.lastSent) > probeBurstGap || a.Departure.Before(b.lastSent)) {
			best = max(best, b.rate())
			b = probeBurst{}
		}
		b.add(a)
	}
	return max(best, b.rate())
}

func (b *probeBurst) add(a Acknowledgment) {
	if b.packets == 0 {
		b.firstSent, b.firstSentSize = a.Departure, a.Size
	}
	b.packets++
	b.bytes += a.Size
	b.lastSent = a.Departure
	if a.Arrival.IsZero() {
		b.lost = true
		return
	}
	if b.firstArrival.IsZero() || a.Arrival.Before(b.firstArrival) {
		b.firstArrival, b.firstArrivalSize = a.Arrival, a.Size
	}
	if a.Arrival.After(b.lastArrival) {
		b.lastArrival = a.Arrival
	}
}

func (b *probeBurst) rate() int {
	send, recv := b.lastSent.Sub(b.firstSent), b.lastArrival.Sub(b.firstArrival)
	if b.packets < probeMinPackets || b.lost || send < probeMinInterval || recv < probeMinInterval {
		return 0
	}
	sendBps := float64(8*(b.bytes-b.firstSentSize)) / send.Seconds()
	recvBps := float64(8*(b.bytes-b.firstArrivalSize)) / recv.Seconds()
	return int(min(sendBps, recvBps))
}
