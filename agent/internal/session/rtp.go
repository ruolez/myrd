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

	"github.com/ruolez/remotectl/agent/internal/platform"
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

// videoTrack sends H.264 access units, or VP9 frames for full color, as RTP
// stamped with their capture time. TrackLocalStaticSample stamps a frame with
// the previous frame's duration, so after every pause the receiver saw the
// first new frame as late by the pause. Both codecs share the SSRC, sequence
// and clock; the payload type tells the browser which decoder to use.
type videoTrack struct {
	*webrtc.TrackLocalStaticRTP

	bound     chan struct{} // closed by the first Bind
	boundOnce sync.Once

	mu     sync.Mutex
	fmtp   string // negotiated H.264 fmtp line
	h264PT uint8
	vp9PT  uint8 // VP9 profile 1 (4:4:4); 0 when not negotiated
	ssrc   uint32
	out    webrtc.TrackLocalWriter
	h264   codecs.H264Payloader
	vp9    codecs.VP9Payloader
	seq    rtp.Sequencer
	clock  rtpClock
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

// Bind records the codecs the browser negotiated: H.264 (the one pion binds)
// and VP9 profile 1 when it is there too.
func (t *videoTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	c, err := t.TrackLocalStaticRTP.Bind(ctx)
	if err == nil {
		t.mu.Lock()
		t.fmtp, t.h264PT, t.vp9PT = c.SDPFmtpLine, uint8(c.PayloadType), FullColorPT(ctx.CodecParameters())
		t.ssrc, t.out = uint32(ctx.SSRC()), ctx.WriteStream()
		t.mu.Unlock()
		t.boundOnce.Do(func() { close(t.bound) })
	}
	return c, err
}

// FullColorPT is the payload type of VP9 profile 1 among the negotiated
// codecs, 0 when the browser did not offer it.
func FullColorPT(negotiated []webrtc.RTPCodecParameters) uint8 {
	for _, c := range negotiated {
		if strings.EqualFold(c.MimeType, webrtc.MimeTypeVP9) && fmtpValue(c.SDPFmtpLine, "profile-id") == "1" {
			return uint8(c.PayloadType)
		}
	}
	return 0
}

// fullColor reports whether VP9 profile 1 was negotiated.
func (t *videoTrack) fullColor() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vp9PT != 0
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

var errNotBound = errors.New("video track not bound")

// WriteFrame packetizes one frame of codec (an Annex-B access unit or a VP9
// frame) captured at ptsMicros; fallback is the frame spacing used when the
// capture clock restarts.
func (t *videoTrack) WriteFrame(data []byte, ptsMicros int64, fallback time.Duration, codec int) error {
	t.mu.Lock()
	out, pt := t.out, t.h264PT
	var payloads [][]byte
	if codec == platform.CodecVP9 {
		pt = t.vp9PT
		payloads = t.vp9.Payload(rtpMTU-rtpHeaderSize, data)
	} else {
		payloads = t.h264.Payload(rtpMTU-rtpHeaderSize, data)
	}
	if out == nil || pt == 0 {
		t.mu.Unlock()
		return errNotBound
	}
	ts := t.clock.stamp(ptsMicros, fallback)
	hdrs := make([]rtp.Header, len(payloads))
	for i := range payloads {
		hdrs[i] = rtp.Header{Version: 2, Marker: i == len(payloads)-1, PayloadType: pt,
			SequenceNumber: t.seq.NextSequenceNumber(), Timestamp: ts, SSRC: t.ssrc}
	}
	t.mu.Unlock()
	var errs []error
	for i := range hdrs {
		if _, err := out.WriteRTP(&hdrs[i], payloads[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func fmtpValue(fmtp, key string) string {
	for _, kv := range strings.Split(fmtp, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok && strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ProfileIDC reads profile_idc from an H.264 fmtp line's profile-level-id;
// anything unreadable is constrained baseline.
func ProfileIDC(fmtp string) int {
	v := fmtpValue(fmtp, "profile-level-id")
	if len(v) == 6 {
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
