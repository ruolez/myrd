package session

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// trackContext binds a videoTrack without a PeerConnection and records
// what it writes.
type trackContext struct {
	codec webrtc.RTPCodecParameters
	mu    sync.Mutex
	sent  []rtp.Header
}

func (c *trackContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{c.codec}
}
func (c *trackContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (c *trackContext) SSRC() webrtc.SSRC                                      { return 1234 }
func (c *trackContext) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (c *trackContext) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (c *trackContext) WriteStream() webrtc.TrackLocalWriter                   { return c }
func (c *trackContext) ID() string                                             { return "test" }
func (c *trackContext) RTCPReader() interceptor.RTCPReader                     { return nil }
func (c *trackContext) Write(b []byte) (int, error)                            { return len(b), nil }

func (c *trackContext) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	c.mu.Lock()
	c.sent = append(c.sent, h.Clone())
	c.mu.Unlock()
	return len(payload), nil
}

func h264Codec(profileLevelID string) webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{PayloadType: 106, RTPCodecCapability: webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=" + profileLevelID}}
}

var (
	idrAU   = []byte{0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80, 0, 0, 0, 1, 0x65, 0x88, 0x84}
	sliceAU = []byte{0, 0, 0, 1, 0x41, 0x9a, 0x02}
)

func TestVideoTrackStampsEachFrameWithItsCaptureTime(t *testing.T) {
	tr, err := newVideoTrack()
	if err != nil {
		t.Fatal(err)
	}
	ctx := &trackContext{codec: h264Codec("640c1f")}
	if _, err := tr.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	// 60 fps, then a 900 ms pause (a static screen), then 60 fps again.
	ptsMicros := []int64{5_000, 21_000, 37_000, 937_000, 953_000}
	for i, pts := range ptsMicros {
		au := sliceAU
		if i == 0 {
			au = idrAU
		}
		if err := tr.WriteFrame(au, pts, 16*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}

	var frames [][]rtp.Header // packets grouped by marker
	var cur []rtp.Header
	for _, h := range ctx.sent {
		cur = append(cur, h)
		if h.Marker {
			frames = append(frames, cur)
			cur = nil
		}
	}
	if len(frames) != len(ptsMicros) || cur != nil {
		t.Fatalf("got %d complete frames (+%d stray packets), want %d", len(frames), len(cur), len(ptsMicros))
	}
	base := frames[0][0].Timestamp
	for i, f := range frames {
		want := uint32((ptsMicros[i] - ptsMicros[0]) * 90 / 1000)
		for _, h := range f {
			if got := h.Timestamp - base; got != want {
				t.Errorf("frame %d: timestamp +%d, want +%d (capture time at 90 kHz)", i, got, want)
			}
		}
	}
	for i := 1; i < len(ctx.sent); i++ {
		if ctx.sent[i].SequenceNumber != ctx.sent[i-1].SequenceNumber+1 {
			t.Fatalf("sequence numbers not consecutive at packet %d", i)
		}
	}
	if got := tr.profile(0); got != ProfileHigh {
		t.Errorf("profile = %d, want %d from the negotiated 640c1f", got, ProfileHigh)
	}
}

func TestRTPClockContinuesAfterTheCaptureClockRestarts(t *testing.T) {
	const fallback = 20 * time.Millisecond // 1800 ticks
	c := rtpClock{anchorTS: 1000}
	got := []uint32{
		c.stamp(50_000, fallback),
		c.stamp(60_000, fallback), // +10 ms
		c.stamp(0, fallback),      // restart: pts went back
		c.stamp(10_000, fallback), // +10 ms on the new clock
		c.stamp(10_000, fallback), // repeated pts
		c.stamp(10_000+int64(rtpMaxGap/time.Microsecond)+1, fallback), // implausible jump
	}
	want := []uint32{1000, 1900, 3700, 4600, 6400, 8200}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stamps = %v, want %v", got, want)
		}
	}
}

func TestRTPClockTracksCaptureTimeAcrossWraparound(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for run := 0; run < 50; run++ {
		c := rtpClock{anchorTS: ^uint32(0) - r.Uint32N(100_000)}
		pts := int64(r.Uint32N(1 << 30))
		first := c.stamp(pts, time.Millisecond)
		start := pts
		for i := 0; i < 200; i++ {
			pts += int64(1 + r.Uint32N(2_000_000)) // up to 2 s between frames
			if got, want := c.stamp(pts, time.Millisecond)-first, uint32((pts-start)*9/100); got != want {
				t.Fatalf("run %d frame %d: +%d ticks, want +%d", run, i, got, want)
			}
		}
	}
}

func TestProfileIDC(t *testing.T) {
	cases := []struct {
		fmtp string
		want int
	}{
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", ProfileBaseline},
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=4d001f", ProfileMain},
		{"profile-level-id=640c1f;packetization-mode=1", ProfileHigh},
		{"PROFILE-LEVEL-ID=640C1F", ProfileHigh},
		{"", ProfileBaseline},
		{"packetization-mode=1", ProfileBaseline},
		{"profile-level-id=64", ProfileBaseline},
		{"profile-level-id=zz001f", ProfileBaseline},
	}
	for _, c := range cases {
		if got := ProfileIDC(c.fmtp); got != c.want {
			t.Errorf("ProfileIDC(%q) = %d, want %d", c.fmtp, got, c.want)
		}
	}
}

type headerSink struct {
	mu   sync.Mutex
	sent []rtp.Header
}

func (s *headerSink) Write(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
	s.mu.Lock()
	s.sent = append(s.sent, h.Clone())
	s.mu.Unlock()
	return len(payload), nil
}

func TestPlayoutDelayTagsPacketsOnlyWhenNegotiated(t *testing.T) {
	const extID = 9
	pd := &playoutDelay{}
	cases := []struct {
		name string
		exts []interceptor.RTPHeaderExtension
		want []byte
	}{
		{"negotiated", []interceptor.RTPHeaderExtension{{URI: playoutDelayURI, ID: extID}}, []byte{0, 0, 0}},
		{"not negotiated", []interceptor.RTPHeaderExtension{{URI: "urn:ietf:params:rtp-hdrext:toffset", ID: extID}}, nil},
	}
	for _, c := range cases {
		sink := &headerSink{}
		w := pd.BindLocalStream(&interceptor.StreamInfo{RTPHeaderExtensions: c.exts}, sink)
		if _, err := w.Write(&rtp.Header{Version: 2}, []byte{1}, nil); err != nil {
			t.Fatal(err)
		}
		got := sink.sent[0].GetExtension(extID)
		if string(got) != string(c.want) {
			t.Errorf("%s: extension %d = %x, want %x (min=max=0)", c.name, extID, got, c.want)
		}
	}
}
