package session

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/inputproto"
	"github.com/ruolez/remotectl/agent/internal/keymap"
	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/platform/fake"
	"github.com/ruolez/remotectl/proto"
)

const sessionID = "3f0c6c1e-6c55-4c3a-9a39-0a3f8d0e2b11"

type recordingPrompter struct {
	mu     sync.Mutex
	reqs   []ConsentRequest
	answer platform.Consent
}

func (r *recordingPrompter) Prompt(_ context.Context, req ConsentRequest) (platform.Consent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return r.answer, nil
}

func (r *recordingPrompter) calls() []ConsentRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConsentRequest(nil), r.reqs...)
}

// harness wires a Manager to an in-process pion "browser".
type harness struct {
	t        *testing.T
	backend  *fake.Backend
	pins     *pins.Store
	prompter *recordingPrompter
	mgr      *Manager
	devPub   ed25519.PublicKey
	clientK  *ecdsa.PrivateKey
	spki     string

	browser *webrtc.PeerConnection
	input   *webrtc.DataChannel
	control *webrtc.DataChannel
	cursor  *webrtc.DataChannel
	file    *webrtc.DataChannel

	mu          sync.Mutex
	statuses    []string
	states      []proto.SessionState
	ends        []proto.SessionEnd
	answerOK    chan error
	controlMsgs chan map[string]any
	cursorMsgs  chan []byte
	rtpPackets  chan struct{}
	autoAccept  bool
	cands       []string
	fileRecs    []proto.SessionFile
	signalOrder []string // session.file / session.end, as sent
	fileText    chan map[string]any
	fileData    chan []byte
}

func newHarness(t *testing.T, answer platform.Consent, tweaks ...func(*Options)) *harness {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ck, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&ck.PublicKey)
	ps, err := pins.Open(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, backend: fake.New(), pins: ps, prompter: &recordingPrompter{answer: answer},
		devPub: pub, clientK: ck, spki: base64.StdEncoding.EncodeToString(der),
		answerOK: make(chan error, 4), controlMsgs: make(chan map[string]any, 64),
		cursorMsgs: make(chan []byte, 256), rtpPackets: make(chan struct{}, 1),
		fileText: make(chan map[string]any, 256), fileData: make(chan []byte, 1024)}
	opts := Options{
		Backend: h.backend, DeviceKey: priv, Pins: ps, Send: h.fromAgent, Prompter: h.prompter,
		AutoAccept: func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.autoAccept },
		Keymap:     keymap.X11, ConsentTimeout: 5 * time.Second, AgentVersion: "test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, tw := range tweaks {
		tw(&opts)
	}
	h.mgr, err = NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.mgr.Close()
		if h.browser != nil {
			h.browser.Close()
		}
	})
	return h
}

func (h *harness) fromAgent(typ string, msg any) error {
	b, _ := proto.Encode(typ, msg)
	switch typ {
	case proto.TypeSessionStatus:
		var m proto.SessionStatus
		_ = json.Unmarshal(b, &m)
		h.mu.Lock()
		h.statuses = append(h.statuses, m.Status)
		h.mu.Unlock()
	case proto.TypeSessionAnswer:
		var m proto.SessionAnswer
		_ = json.Unmarshal(b, &m)
		go func() { h.answerOK <- h.applyAnswer(m) }()
	case proto.TypeICECandidate:
		var m proto.ICECandidateMsg
		_ = json.Unmarshal(b, &m)
		if m.Candidate != nil {
			c := m.Candidate
			h.mu.Lock()
			h.cands = append(h.cands, c.Candidate)
			h.mu.Unlock()
			go func() {
				// Candidates may race the answer; retry briefly like a browser queue would.
				for i := 0; i < 50; i++ {
					if h.browser.RemoteDescription() != nil {
						_ = h.browser.AddICECandidate(webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: c.SDPMid, SDPMLineIndex: c.SDPMLineIndex})
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
	case proto.TypeSessionState:
		var m proto.SessionState
		_ = json.Unmarshal(b, &m)
		h.mu.Lock()
		h.states = append(h.states, m)
		h.mu.Unlock()
	case proto.TypeSessionEnd:
		var m proto.SessionEnd
		_ = json.Unmarshal(b, &m)
		h.mu.Lock()
		h.ends = append(h.ends, m)
		h.signalOrder = append(h.signalOrder, typ)
		h.mu.Unlock()
	case proto.TypeSessionFile:
		var m proto.SessionFile
		_ = json.Unmarshal(b, &m)
		h.mu.Lock()
		h.fileRecs = append(h.fileRecs, m)
		h.signalOrder = append(h.signalOrder, typ)
		h.mu.Unlock()
	}
	return nil
}

func (h *harness) applyAnswer(m proto.SessionAnswer) error {
	fp, err := proto.SDPFingerprint(m.SDP)
	if err != nil {
		return err
	}
	if err := proto.VerifyDeviceSig(base64.StdEncoding.EncodeToString(h.devPub), m.FPSig,
		proto.FingerprintMessage(sessionID, proto.RoleAnswer, fp)); err != nil {
		return err
	}
	return h.browser.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
}

func (h *harness) signFP(fp string) string {
	d := sha256.Sum256(proto.FingerprintMessage(sessionID, proto.RoleOffer, fp))
	r, s, _ := ecdsa.Sign(rand.Reader, h.clientK, d[:])
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return base64.StdEncoding.EncodeToString(sig)
}

// offer builds the browser peer and sends its offer. tamper swaps the signed fingerprint.
func (h *harness) offer(tamper bool) {
	t := h.t
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	h.browser = pc
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	ordered := false
	zero := uint16(0)
	h.input, _ = pc.CreateDataChannel("input", nil)
	h.cursor, _ = pc.CreateDataChannel("cursor", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &zero})
	h.control, _ = pc.CreateDataChannel("control", nil)
	h.file, _ = pc.CreateDataChannel("file", nil)
	h.file.OnMessage(func(m webrtc.DataChannelMessage) {
		if !m.IsString {
			h.fileData <- m.Data
			return
		}
		var v map[string]any
		if json.Unmarshal(m.Data, &v) == nil {
			h.fileText <- v
		}
	})
	h.control.OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			var v map[string]any
			if json.Unmarshal(m.Data, &v) == nil {
				select {
				case h.controlMsgs <- v:
				default:
				}
			}
		}
	})
	h.cursor.OnMessage(func(m webrtc.DataChannelMessage) {
		select {
		case h.cursorMsgs <- m.Data:
		default:
		}
	})
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			select {
			case h.rtpPackets <- struct{}{}:
			default:
			}
		}
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
		}
	})
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		b, _ := proto.Encode(proto.TypeICECandidate, proto.ICECandidateMsg{SessionID: sessionID,
			Candidate: &proto.ICECandidate{Candidate: init.Candidate, SDPMid: init.SDPMid, SDPMLineIndex: init.SDPMLineIndex}})
		h.mgr.HandleSignal(proto.TypeICECandidate, b)
	})
	off, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(off); err != nil {
		t.Fatal(err)
	}
	fp, err := proto.SDPFingerprint(pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		fp = strings.Repeat("AB:", 31) + "AB"
	}
	b, _ := proto.Encode(proto.TypeSessionOffer, proto.SessionOffer{SessionID: sessionID, SDP: pc.LocalDescription().SDP,
		FPSig: h.signFP(fp), ClientKey: &proto.ClientKeyInfo{ID: "ck1", PublicKey: h.spki, Label: "Test Chrome",
			CreatedAt: "2026-10-19T00:00:00Z"}, User: &proto.UserInfo{Email: "owner@example.com"}})
	h.mgr.HandleSignal(proto.TypeSessionOffer, b)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) waitControl(t *testing.T, kind string) map[string]any {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m := <-h.controlMsgs:
			if m["t"] == kind {
				return m
			}
		case <-timeout:
			t.Fatalf("no control %q message", kind)
		}
	}
}

func (h *harness) snapshot() (statuses []string, states []proto.SessionState, ends []proto.SessionEnd) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.statuses...), append([]proto.SessionState(nil), h.states...),
		append([]proto.SessionEnd(nil), h.ends...)
}

func TestLoopbackSessionNewBrowserApprovedAndRemembered(t *testing.T) {
	h := newHarness(t, platform.ConsentRemember)
	h.offer(false)

	select {
	case err := <-h.answerOK:
		if err != nil {
			t.Fatalf("answer rejected by browser: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
	}
	waitFor(t, "session.state connected", func() bool { _, st, _ := h.snapshot(); return len(st) > 0 })

	statuses, states, _ := h.snapshot()
	if strings.Join(statuses, ",") != "awaiting_approval,approved" {
		t.Fatalf("statuses = %v", statuses)
	}
	// Direct, not relayed. Host vs srflx depends on trickle timing: a remote
	// candidate seen in a STUN check before it is signalled is peer-reflexive.
	if !states[0].Connected || states[0].Path == proto.PathRelay {
		t.Fatalf("state = %+v", states[0])
	}
	calls := h.prompter.calls()
	if len(calls) != 1 || !calls[0].NewKey || calls[0].Label != "Test Chrome" || calls[0].Email != "owner@example.com" {
		t.Fatalf("prompt calls = %+v", calls)
	}
	if !h.pins.IsPinned(h.spki) {
		t.Fatal("client key not pinned after Allow and remember")
	}

	hello := h.waitControl(t, "hello")
	if hello["codec"] != "h264" || hello["encoder"] != "fake" {
		t.Fatalf("hello = %v", hello)
	}

	select {
	case <-h.rtpPackets:
	case <-time.After(10 * time.Second):
		t.Fatal("no RTP video received")
	}

	waitFor(t, "input channel open", func() bool { return h.input.ReadyState() == webrtc.DataChannelStateOpen })
	_ = h.input.Send(inputproto.Encode(inputproto.Event{Type: inputproto.TypeKey, Usage: 0x04, Down: true}))
	_ = h.input.Send(inputproto.Encode(inputproto.Event{Type: inputproto.TypeMouseMove, X: 100, Y: 200, Monitor: 0}))
	_ = h.input.Send(inputproto.Encode(inputproto.Event{Type: inputproto.TypeText, Text: "hé"}))
	waitFor(t, "input injected", func() bool { return len(h.backend.Injected()) >= 2 && len(h.backend.Texts()) == 1 })
	inj := h.backend.Injected()
	if inj[0] != (platform.InputEvent{Type: platform.EvKey, Keycode: 38, Down: true}) {
		t.Fatalf("key event = %+v", inj[0])
	}
	if inj[1] != (platform.InputEvent{Type: platform.EvMouseMove, MonitorID: 1, X: 100, Y: 200}) {
		t.Fatalf("move event = %+v", inj[1])
	}

	_ = h.control.SendText(`{"t":"ping","id":42,"ts":1000}`)
	pong := h.waitControl(t, "pong")
	if pong["id"] != float64(42) || pong["ts"] != float64(1000) {
		t.Fatalf("pong = %v", pong)
	}

	timeout := time.After(10 * time.Second)
	gotShape, gotPos := false, false
	for !(gotShape && gotPos) {
		select {
		case b := <-h.cursorMsgs:
			typ, _, _, _, _, err := inputproto.DecodeCursor(b)
			if err == nil {
				gotShape = gotShape || typ == inputproto.TypeCursorShape
				gotPos = gotPos || typ == inputproto.TypeCursorPos
			}
		case <-timeout:
			t.Fatalf("cursor channel: shape=%v pos=%v", gotShape, gotPos)
		}
	}
	if !h.backend.IndicatorOn() || !h.backend.Capturing() {
		t.Fatal("indicator and capture should be on mid-session")
	}
	if got := h.mgr.ActiveSessions(); len(got) != 1 || got[0] != sessionID {
		t.Fatalf("active sessions = %v", got)
	}

	end, _ := proto.Encode(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sessionID, Reason: proto.EndUserHangup})
	h.mgr.HandleSignal(proto.TypeSessionEnd, end)
	waitFor(t, "teardown", func() bool { return !h.backend.Capturing() && !h.backend.IndicatorOn() })
	_, _, ends := h.snapshot()
	if len(ends) != 1 || ends[0].Reason != proto.EndUserHangup || ends[0].BytesSent == 0 {
		t.Fatalf("ends = %+v", ends)
	}
	// The held KeyA was released on teardown.
	inj = h.backend.Injected()
	if last := inj[len(inj)-1]; last != (platform.InputEvent{Type: platform.EvKey, Keycode: 38, Down: false}) {
		t.Fatalf("last injected = %+v, want KeyA up", last)
	}
	if h.mgr.Current() != nil {
		t.Fatal("manager still has a session")
	}
}

func TestSwappedFingerprintRejected(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.offer(true)
	waitFor(t, "session.end", func() bool { _, _, e := h.snapshot(); return len(e) > 0 })
	statuses, _, ends := h.snapshot()
	if ends[0].Reason != proto.EndBadSignature || len(statuses) != 0 || len(h.prompter.calls()) != 0 {
		t.Fatalf("ends=%+v statuses=%v prompts=%d", ends, statuses, len(h.prompter.calls()))
	}
	select {
	case <-h.answerOK:
		t.Fatal("agent answered a tampered offer")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPinnedAutoAcceptSkipsPrompt(t *testing.T) {
	h := newHarness(t, platform.ConsentDeny)
	_ = h.pins.Pin(h.spki, "Test Chrome", time.Now())
	h.autoAccept = true
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	statuses, _, _ := h.snapshot()
	if len(h.prompter.calls()) != 0 || strings.Join(statuses, ",") != "approved" {
		t.Fatalf("prompts=%d statuses=%v", len(h.prompter.calls()), statuses)
	}
}

func TestPinnedWithoutAutoAcceptPromptsLightly(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	_ = h.pins.Pin(h.spki, "Test Chrome", time.Now())
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	calls := h.prompter.calls()
	if len(calls) != 1 || calls[0].NewKey || calls[0].Title != "Remote session request" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestDeniedConsentEndsNotApproved(t *testing.T) {
	h := newHarness(t, platform.ConsentDeny)
	h.offer(false)
	waitFor(t, "session.end", func() bool { _, _, e := h.snapshot(); return len(e) > 0 })
	_, _, ends := h.snapshot()
	if ends[0].Reason != proto.EndNotApproved || h.pins.IsPinned(h.spki) {
		t.Fatalf("ends = %+v", ends)
	}
}

func TestRevokedKeyRefusedWithoutPrompt(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	_ = h.pins.Pin(h.spki, "Test Chrome", time.Now())
	h.mgr.SetRevoked([]string{h.spki})
	h.offer(false)
	waitFor(t, "session.end", func() bool { _, _, e := h.snapshot(); return len(e) > 0 })
	_, _, ends := h.snapshot()
	if ends[0].Reason != proto.EndNotApproved || len(h.prompter.calls()) != 0 || h.pins.IsPinned(h.spki) {
		t.Fatalf("ends=%+v prompts=%d", ends, len(h.prompter.calls()))
	}
}

func TestMissingPermissionEndsSession(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.backend.Perms = platform.Permissions{Screen: false, Input: true}
	h.offer(false)
	waitFor(t, "session.end", func() bool { _, _, e := h.snapshot(); return len(e) > 0 })
	if _, _, ends := h.snapshot(); ends[0].Reason != proto.EndPermission {
		t.Fatalf("ends = %+v", ends)
	}
}

func TestSingleUDPPortAndExtraHostIP(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce, func(o *Options) {
		o.UDPPort = 45961
		o.NAT1To1IPs = []string{"10.99.99.99"}
	})
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "extra host candidate", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, c := range h.cands {
			if strings.Contains(c, "10.99.99.99 45961 typ host") {
				return true
			}
		}
		return false
	})
	waitFor(t, "connected over the mux port", func() bool { _, st, _ := h.snapshot(); return len(st) > 0 })
}
