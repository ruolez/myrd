package session

import (
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/flexfec"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// Forward error correction: FlexFEC-03 repair packets on their own SSRC, so a
// lost packet is rebuilt in the browser without waiting a round trip for a
// retransmission. Repair packets protect one frame at a time (the frame's
// last packet is never stuck waiting for the next frame) and carry
// transport-wide sequence numbers like media, so the pacer spaces them and
// GCC counts them. The interceptor therefore also stamps those numbers, in
// place of pion's TWCC header-extension sender.

const (
	flexFECPT = 112
	// fecGroup is the most media packets one set of repair packets covers;
	// FlexFEC-03 masks reach 109, smaller groups keep repair packets short.
	fecGroup   = 48
	fecMaxRate = 0.5
	fecOnLoss  = 0.01  // start protecting at 1% loss
	fecOffLoss = 0.005 // and stop below 0.5% ...
	fecOffHold = 5 * time.Second
	fecSamples = 5 // the policy follows the median of this many 1 s loss samples
)

// FECRate is the repair overhead (repair packets per media packet) for a
// loss fraction: three repair packets per expected loss, 10% to 50%.
func FECRate(loss float64) float64 {
	if loss < fecOnLoss {
		return 0
	}
	return math.Min(fecMaxRate, math.Max(0.1, 3*loss))
}

// fecPolicy turns GCC's loss into the protection rate: it follows the
// median of the recent samples (a burst of feedback can read far above the
// link's loss), turns on at fecOnLoss and off only after fecOffHold below
// fecOffLoss.
type fecPolicy struct {
	rate    float64
	quiet   time.Time // when loss fell below fecOffLoss; zero while above
	samples []float64
}

func (f *fecPolicy) next(now time.Time, sample float64) float64 {
	f.samples = append(f.samples, sample)
	if len(f.samples) > fecSamples {
		f.samples = f.samples[1:]
	}
	sorted := slices.Sorted(slices.Values(f.samples))
	loss := sorted[(len(sorted)-1)/2] // the lower median: a lone spike never counts
	switch {
	case loss >= fecOnLoss:
		f.rate, f.quiet = FECRate(loss), time.Time{}
	case f.rate > 0 && loss < fecOffLoss:
		if f.quiet.IsZero() {
			f.quiet = now
		}
		if now.Sub(f.quiet) >= fecOffHold {
			f.rate, f.quiet = 0, time.Time{}
		}
	default:
		f.quiet = time.Time{}
	}
	return f.rate
}

// fecCount is how many repair packets protect n media packets at rate.
func fecCount(n int, rate float64) int {
	if rate <= 0 || n <= 0 {
		return 0
	}
	return min(n, int(math.Ceil(float64(n)*rate)))
}

// fecControl is shared by the session and its interceptor.
type fecControl struct {
	rate       atomic.Uint64 // math.Float64bits of the protection rate
	repair     atomic.Int64  // repair packets sent
	negotiated atomic.Bool   // the browser accepted flexfec-03 (a repair SSRC)
}

func (c *fecControl) setRate(r float64) { c.rate.Store(math.Float64bits(r)) }
func (c *fecControl) getRate() float64  { return math.Float64frombits(c.rate.Load()) }

type fecFactory struct{ ctl *fecControl }

func (f fecFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &fecInterceptor{ctl: f.ctl}, nil
}

// configureFEC registers the transport-wide sequence extension and
// flexfec-03 and adds the interceptor; it takes the place of
// webrtc.ConfigureTWCCHeaderExtensionSender in the registry order.
func configureFEC(me *webrtc.MediaEngine, reg *interceptor.Registry, ctl *fecControl) error {
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.TransportCCURI}, kind); err != nil {
			return err
		}
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{PayloadType: flexFECPT, RTPCodecCapability: webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeFlexFEC03, ClockRate: 90000, SDPFmtpLine: "repair-window=10000000"}}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	reg.Add(fecFactory{ctl})
	return nil
}

type fecInterceptor struct {
	interceptor.NoOp
	ctl     *fecControl
	twccSeq atomic.Uint32
}

func (f *fecInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	var twccID uint8
	for _, e := range info.RTPHeaderExtensions {
		if e.URI == sdp.TransportCCURI {
			twccID = uint8(e.ID)
		}
	}
	st := &fecStream{f: f, twccID: twccID, media: info.SSRC, out: writer}
	if info.PayloadTypeForwardErrorCorrection != 0 && info.SSRCForwardErrorCorrection != 0 {
		st.enc = flexfec.NewFlexEncoder03(info.PayloadTypeForwardErrorCorrection, info.SSRCForwardErrorCorrection)
		f.ctl.negotiated.Store(true)
	}
	return interceptor.RTPWriterFunc(st.write)
}

type fecStream struct {
	f      *fecInterceptor
	twccID uint8
	media  uint32
	out    interceptor.RTPWriter

	mu      sync.Mutex
	enc     *flexfec.FlexEncoder03
	frame   []rtp.Packet // this frame's media packets so far, as sent
	lastSeq uint16
	started bool
}

func (s *fecStream) stamp(h *rtp.Header) error {
	if s.twccID == 0 {
		return nil
	}
	b, err := (&rtp.TransportCCExtension{TransportSequence: uint16(s.f.twccSeq.Add(1) - 1)}).Marshal()
	if err != nil {
		return err
	}
	return h.SetExtension(s.twccID, b)
}

func (s *fecStream) write(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
	if err := s.stamp(h); err != nil {
		return 0, err
	}
	n, err := s.out.Write(h, payload, a)
	if err != nil || s.enc == nil || h.SSRC != s.media {
		return n, err
	}
	rate := s.f.ctl.getRate()
	s.mu.Lock()
	// Retransmissions (an old sequence number) are not protected again.
	fresh := !s.started || int16(h.SequenceNumber-s.lastSeq) > 0
	if fresh {
		s.started, s.lastSeq = true, h.SequenceNumber
	}
	if !fresh || rate <= 0 {
		s.frame = s.frame[:0]
		s.mu.Unlock()
		return n, nil
	}
	if len(s.frame) > 0 && h.SequenceNumber != s.frame[len(s.frame)-1].SequenceNumber+1 {
		s.frame = s.frame[:0] // a gap: the packets cannot be one block
	}
	s.frame = append(s.frame, rtp.Packet{Header: h.Clone(), Payload: append([]byte(nil), payload...)})
	var repair []rtp.Packet
	if h.Marker || len(s.frame) == fecGroup {
		repair = s.enc.EncodeFec(s.frame, uint32(fecCount(len(s.frame), rate)))
		s.frame = s.frame[:0]
	}
	s.mu.Unlock()
	for i := range repair {
		r := &repair[i]
		if err := s.stamp(&r.Header); err != nil {
			return n, err
		}
		if _, err := s.out.Write(&r.Header, r.Payload, a); err != nil {
			return n, err
		}
		s.f.ctl.repair.Add(1)
	}
	return n, nil
}
