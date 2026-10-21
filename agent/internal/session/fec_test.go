package session

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
)

func TestFECRate(t *testing.T) {
	cases := []struct{ loss, want float64 }{
		{0, 0}, {0.009, 0}, {0.01, 0.1}, {0.05, 0.15}, {0.1, 0.3}, {0.3, 0.5},
	}
	for _, c := range cases {
		if got := FECRate(c.loss); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("FECRate(%v) = %v, want %v", c.loss, got, c.want)
		}
	}
}

func TestFECPolicyFollowsTheMedianLoss(t *testing.T) {
	t0 := time.Unix(1000, 0)
	steps := []struct {
		loss float64
		want float64
	}{
		{0.004, 0}, {0.4, 0}, {0, 0}, {0.05, 0}, // one burst of feedback: no protection
		{0.06, 0.15}, // the median reaches 5%: on
		{0.4, 0.18},  // a spike moves it only as far as the median (6%)
		{0.05, 0.15}, {0.007, 0.15}, {0.007, 0.15},
		{0.007, 0.15}, {0.002, 0.15}, {0.002, 0.15}, // between the thresholds: unchanged
		{0.002, 0.15}, // the median falls below 0.5%: a quiet spell starts
		{0.002, 0.15}, {0.002, 0.15}, {0.002, 0.15}, {0.002, 0.15},
		{0.002, 0}, // 5 s quiet: off
	}
	var p fecPolicy
	for i, s := range steps {
		if got := p.next(t0.Add(time.Duration(i)*time.Second), s.loss); math.Abs(got-s.want) > 1e-9 {
			t.Fatalf("step %d (loss %v): rate %v, want %v", i, s.loss, got, s.want)
		}
	}
}

func TestFECCount(t *testing.T) {
	cases := []struct {
		n    int
		rate float64
		want int
	}{{1, 0.1, 1}, {10, 0.1, 1}, {11, 0.1, 2}, {48, 0.5, 24}, {5, 0, 0}, {0, 0.3, 0}, {3, 1, 3}}
	for _, c := range cases {
		if got := fecCount(c.n, c.rate); got != c.want {
			t.Errorf("fecCount(%d, %v) = %d, want %d", c.n, c.rate, got, c.want)
		}
	}
}

// wire records packets as the transport would send them.
type wire struct {
	mu   sync.Mutex
	pkts []rtp.Packet
}

func (w *wire) Write(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
	w.mu.Lock()
	w.pkts = append(w.pkts, rtp.Packet{Header: h.Clone(), Payload: slices.Clone(payload)})
	w.mu.Unlock()
	return len(payload), nil
}

const (
	testMediaSSRC, testFECSSRC = 1111, 2222
	testTWCCID, testPlayoutID  = 5, 9
)

func fecTestStream(rate float64, fec bool) (interceptor.RTPWriter, *wire, *fecControl) {
	ctl := &fecControl{}
	ctl.setRate(rate)
	info := &interceptor.StreamInfo{SSRC: testMediaSSRC, RTPHeaderExtensions: []interceptor.RTPHeaderExtension{
		{URI: sdp.TransportCCURI, ID: testTWCCID}}}
	if fec {
		info.SSRCForwardErrorCorrection, info.PayloadTypeForwardErrorCorrection = testFECSSRC, flexFECPT
	}
	w := &wire{}
	f, _ := fecFactory{ctl}.NewInterceptor("")
	return f.BindLocalStream(info, w), w, ctl
}

// sendFrame writes n media packets of varied sizes, the last with the marker,
// each tagged with another extension (as the playout-delay interceptor does).
func sendFrame(t *testing.T, w interceptor.RTPWriter, seq uint16, n int, ts uint32) uint16 {
	t.Helper()
	for i := 0; i < n; i++ {
		h := &rtp.Header{Version: 2, PayloadType: 106, SequenceNumber: seq, Timestamp: ts, SSRC: testMediaSSRC,
			Marker: i == n-1}
		if err := h.SetExtension(testPlayoutID, []byte{0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte{byte(seq), byte(i)}, 40+int(seq%7)*90)
		if _, err := w.Write(h, payload, nil); err != nil {
			t.Fatal(err)
		}
		seq++
	}
	return seq
}

func twccSeq(t *testing.T, p rtp.Packet) uint16 {
	t.Helper()
	var e rtp.TransportCCExtension
	if err := e.Unmarshal(p.GetExtension(testTWCCID)); err != nil {
		t.Fatalf("packet ssrc %d seq %d has no transport-wide sequence number", p.SSRC, p.SequenceNumber)
	}
	return e.TransportSequence
}

// recoverFlexFEC03 rebuilds the media packet with sequence number lost from a
// FlexFEC-03 repair packet and the other packets it protects (draft-ietf-
// payload-flexible-fec-scheme-03 §4.2, one SSRC); nil when it does not cover lost.
func recoverFlexFEC03(repair rtp.Packet, media map[uint16][]byte, lost uint16) []byte {
	p := repair.Payload
	base := binary.BigEndian.Uint16(p[16:18])
	hdr, covered := 20, []uint16{}
	m1 := binary.BigEndian.Uint16(p[18:20])
	for i := 0; i < 15; i++ {
		if m1&(1<<(14-i)) != 0 {
			covered = append(covered, base+uint16(i))
		}
	}
	if m1&0x8000 == 0 {
		hdr = 24
		m2 := binary.BigEndian.Uint32(p[20:24])
		for i := 0; i < 31; i++ {
			if m2&(1<<(30-i)) != 0 {
				covered = append(covered, base+15+uint16(i))
			}
		}
		if m2&0x80000000 == 0 {
			hdr = 32
			m3 := binary.BigEndian.Uint64(p[24:32])
			for i := 0; i < 63; i++ {
				if m3&(1<<(62-i)) != 0 {
					covered = append(covered, base+46+uint16(i))
				}
			}
		}
	}
	if !slices.Contains(covered, lost) {
		return nil
	}
	fixed := slices.Clone(p[:8])
	body := slices.Clone(p[hdr:])
	for _, sn := range covered {
		if sn == lost {
			continue
		}
		b := media[sn]
		fixed[0] ^= b[0]
		fixed[1] ^= b[1]
		l := uint16(len(b) - 12)
		fixed[2] ^= byte(l >> 8)
		fixed[3] ^= byte(l)
		for i := 4; i < 8; i++ {
			fixed[i] ^= b[i]
		}
		for i := 12; i < len(b); i++ {
			body[i-12] ^= b[i]
		}
	}
	out := []byte{fixed[0]&0x3f | 0x80, fixed[1], byte(lost >> 8), byte(lost)}
	out = append(out, fixed[4:8]...)
	out = append(out, p[12:16]...)
	return append(out, body[:binary.BigEndian.Uint16(fixed[2:4])]...)
}

func TestFECRepairsEachFrameAndEveryLostPacket(t *testing.T) {
	w, out, ctl := fecTestStream(0.25, true)
	seq := sendFrame(t, w, 65530, 8, 1000) // wraps the sequence number
	sendFrame(t, w, seq, 3, 4000)

	// Frame 1: 8 media then 2 repair packets, before frame 2 starts.
	var kinds []string
	media := map[uint16][]byte{}
	var repair []rtp.Packet
	for i, p := range out.pkts {
		if twccSeq(t, p) != uint16(i) {
			t.Fatalf("packet %d carries transport sequence %d: media and repair share one counter", i, twccSeq(t, p))
		}
		switch p.SSRC {
		case testMediaSSRC:
			kinds = append(kinds, "m")
			b, _ := p.Marshal()
			media[p.SequenceNumber] = b
		case testFECSSRC:
			kinds = append(kinds, "r")
			if p.PayloadType != flexFECPT {
				t.Fatalf("repair payload type %d", p.PayloadType)
			}
			repair = append(repair, p)
		}
	}
	if got := slices.Compact(slices.Clone(kinds)); !slices.Equal(kinds, []string{"m", "m", "m", "m", "m", "m", "m", "m", "r", "r", "m", "m", "m", "r"}) {
		t.Fatalf("packet order %v (%v): want each frame followed by its repair packets", kinds, got)
	}
	if ctl.repair.Load() != 3 {
		t.Fatalf("repair counter %d, want 3", ctl.repair.Load())
	}
	// Every media packet can be rebuilt from one repair packet and the rest.
	for sn, want := range media {
		var got []byte
		for _, r := range repair {
			if got = recoverFlexFEC03(r, media, sn); got != nil {
				break
			}
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("seq %d: recovered %x\nwant %x", sn, got, want)
		}
	}
}

func TestFECCoversLargeFramesInGroups(t *testing.T) {
	w, out, _ := fecTestStream(0.1, true)
	sendFrame(t, w, 100, 120, 1000)
	media := map[uint16][]byte{}
	var repair []rtp.Packet
	for _, p := range out.pkts {
		if p.SSRC == testFECSSRC {
			repair = append(repair, p)
			continue
		}
		b, _ := p.Marshal()
		media[p.SequenceNumber] = b
	}
	// Groups of 48, 48 and 24 packets at 10%: 5 + 5 + 3 repair packets.
	if len(repair) != 13 {
		t.Fatalf("%d repair packets, want 13", len(repair))
	}
	for _, sn := range []uint16{100, 147, 148, 195, 196, 219} {
		var got []byte
		for _, r := range repair {
			if got = recoverFlexFEC03(r, media, sn); got != nil {
				break
			}
		}
		if !bytes.Equal(got, media[sn]) {
			t.Fatalf("seq %d not recoverable", sn)
		}
	}
}

func TestFECLeavesRetransmissionsAndCleanLinksAlone(t *testing.T) {
	cases := []struct {
		name string
		rate float64
		fec  bool
	}{{"clean link", 0, true}, {"browser without FlexFEC", 0.5, false}}
	for _, c := range cases {
		w, out, _ := fecTestStream(c.rate, c.fec)
		sendFrame(t, w, 10, 5, 1000)
		for _, p := range out.pkts {
			if p.SSRC != testMediaSSRC {
				t.Fatalf("%s: repair packet sent", c.name)
			}
		}
		if len(out.pkts) != 5 || twccSeq(t, out.pkts[4]) != 4 {
			t.Fatalf("%s: %d packets", c.name, len(out.pkts))
		}
	}

	w, out, _ := fecTestStream(0.5, true)
	seq := sendFrame(t, w, 10, 4, 1000)
	n := len(out.pkts)
	sendFrame(t, w, 11, 1, 1000) // a NACKed packet sent again
	if len(out.pkts) != n+1 || out.pkts[n].SSRC != testMediaSSRC || twccSeq(t, out.pkts[n]) != uint16(n) {
		t.Fatalf("retransmission: %d new packets; want it alone, with a fresh transport sequence", len(out.pkts)-n)
	}
	sendFrame(t, w, seq, 2, 2000)
	if last := out.pkts[len(out.pkts)-1]; last.SSRC != testFECSSRC {
		t.Fatal("protection did not resume after the retransmission")
	}
}

// lossyEstimator reports a fixed loss to the media pipe.
type lossyEstimator struct{ loss float64 }

func (e lossyEstimator) AddStream(_ *interceptor.StreamInfo, w interceptor.RTPWriter) interceptor.RTPWriter {
	return w
}
func (lossyEstimator) WriteRTCP([]rtcp.Packet, interceptor.Attributes) error { return nil }
func (lossyEstimator) GetTargetBitrate() int                                 { return 3_000_000 }
func (lossyEstimator) OnTargetBitrateChange(func(int))                       {}
func (e lossyEstimator) GetStats() map[string]any                            { return map[string]any{"averageLoss": e.loss} }
func (lossyEstimator) Close() error                                          { return nil }

func TestFECOnlyWhenTheBrowserNegotiatedIt(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, negotiated := range []bool{false, true} {
		s := &sess{m: &Manager{}, fec: &fecControl{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		s.fec.negotiated.Store(negotiated)
		p := &mediaPipe{s: s, now: func() time.Time { return now }, est: lossyEstimator{0.05}, bweKbps: 3000}
		for range fecSamples {
			p.updateFEC()
		}
		rate, budget := s.fec.getRate(), p.inputLocked().BweKbps
		if negotiated && (rate != FECRate(0.05) || budget != int(3000/(1+FECRate(0.05)))) {
			t.Errorf("negotiated: rate %v, encoder budget %d kbps; want %v and the estimate less the repair", rate, budget, FECRate(0.05))
		}
		if !negotiated && (rate != 0 || budget != 3000) {
			t.Errorf("not negotiated: rate %v, encoder budget %d kbps; want no repair and the whole estimate", rate, budget)
		}
	}
}
