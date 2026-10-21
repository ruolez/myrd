package session

import (
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

const (
	rtpMTU        = 1200 // pion's outbound MTU
	rtpHeaderSize = 12
	rtpMaxGap     = 10 * time.Minute // a larger jump is a clock change, not a pause
)

// H.264 profile_idc values (rc_capture_config.profile_idc).
const (
	ProfileBaseline = 66
	ProfileMain     = 77
	ProfileHigh     = 100
)

// videoTrack sends H.264 access units as RTP stamped with their capture time.
// TrackLocalStaticSample stamps a frame with the previous frame's duration, so
// after every pause the receiver saw the first new frame as late by the pause.
type videoTrack struct {
	*webrtc.TrackLocalStaticRTP

	bound     chan struct{} // closed by the first Bind
	boundOnce sync.Once

	mu        sync.Mutex
	fmtp      string // negotiated H.264 fmtp line
	payloader codecs.H264Payloader
	seq       rtp.Sequencer
	clock     rtpClock
}

func newVideoTrack() (*videoTrack, error) {
	t, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "remotectl")
	if err != nil {
		return nil, err
	}
	return &videoTrack{TrackLocalStaticRTP: t, bound: make(chan struct{}), seq: rtp.NewRandomSequencer(),
		clock: rtpClock{anchorTS: rand.Uint32()}}, nil
}

// Bind records the codec the browser negotiated.
func (t *videoTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	c, err := t.TrackLocalStaticRTP.Bind(ctx)
	if err == nil {
		t.mu.Lock()
		t.fmtp = c.SDPFmtpLine
		t.mu.Unlock()
		t.boundOnce.Do(func() { close(t.bound) })
	}
	return c, err
}

// profile is the negotiated H.264 profile_idc. pion binds the track just
// after the connection reports connected, which is when capture starts, so
// it waits up to wait for the binding; unbound means constrained baseline.
func (t *videoTrack) profile(wait time.Duration) int {
	select {
	case <-t.bound:
	case <-time.After(wait):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return ProfileIDC(t.fmtp)
}

// WriteFrame packetizes one Annex-B access unit captured at ptsMicros;
// fallback is the frame spacing used when the capture clock restarts.
func (t *videoTrack) WriteFrame(data []byte, ptsMicros int64, fallback time.Duration) error {
	t.mu.Lock()
	ts := t.clock.stamp(ptsMicros, fallback)
	payloads := t.payloader.Payload(rtpMTU-rtpHeaderSize, data)
	pkts := make([]*rtp.Packet, len(payloads))
	for i, pl := range payloads {
		pkts[i] = &rtp.Packet{Header: rtp.Header{Version: 2, Marker: i == len(payloads)-1,
			SequenceNumber: t.seq.NextSequenceNumber(), Timestamp: ts}, Payload: pl}
	}
	t.mu.Unlock()
	var errs []error
	for _, p := range pkts {
		if err := t.WriteRTP(p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ProfileIDC reads profile_idc from an H.264 fmtp line's profile-level-id;
// anything unreadable is constrained baseline.
func ProfileIDC(fmtp string) int {
	for _, kv := range strings.Split(fmtp, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || !strings.EqualFold(k, "profile-level-id") || len(v) != 6 {
			continue
		}
		if idc, err := strconv.ParseUint(v[:2], 16, 8); err == nil {
			return int(idc)
		}
	}
	return ProfileBaseline
}

// rtpClock maps capture timestamps (µs) to the 90 kHz RTP clock. A pts that
// does not move forward, or jumps by more than rtpMaxGap, means the capture
// clock restarted: the stamps continue one frame after the last one.
type rtpClock struct {
	anchorPTS, lastPTS int64
	anchorTS, lastTS   uint32
	started            bool
}

func (c *rtpClock) stamp(pts int64, fallback time.Duration) uint32 {
	if !c.started {
		c.started, c.anchorPTS, c.lastPTS, c.lastTS = true, pts, pts, c.anchorTS
		return c.anchorTS
	}
	if pts <= c.lastPTS || time.Duration(pts-c.lastPTS)*time.Microsecond > rtpMaxGap {
		c.anchorTS = c.lastTS + uint32(fallback*90000/time.Second)
		c.anchorPTS = pts
	}
	ts := c.anchorTS + uint32((pts-c.anchorPTS)*9/100)
	c.lastPTS, c.lastTS = pts, ts
	return ts
}
