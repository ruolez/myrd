package session

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/agent/internal/gcc"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

const (
	stateAwaiting   = proto.StatusAwaitingApproval
	stateConnecting = "connecting"
	stateActive     = "active"
	stateEnded      = "ended"
)

// H.264 fmtp lines we accept; the browser's order picks one and the encoder
// follows it (ProfileIDC).
var h264Profiles = []string{"42e01f", "4d001f", "640c1f", "64001f"}

// Full color is VP9 profile 1 (4:4:4) on its own payload type, negotiated
// next to H.264 and switched to mid-session.
const fullColorPT = 110

type sess struct {
	m          *Manager
	id         string
	clientKey  proto.ClientKeyInfo
	userID     string
	email      string
	iceServers []webrtc.ICEServer
	log        *slog.Logger
	ctx        context.Context
	cancel     context.CancelFunc
	since      time.Time
	done       chan struct{}

	negMu sync.Mutex // serializes offer/answer exchanges

	mu           sync.Mutex
	state        string
	pc           *webrtc.PeerConnection
	remoteSet    bool
	pendingCands []webrtc.ICECandidateInit
	path         string
	ended        bool
	started      bool // media pipeline started (first connect)

	media *mediaPipe
	fec   *fecControl
	in    *inputState
	files *files.Service // set when the browser opens the file channel
}

func newSess(m *Manager, o proto.SessionOffer) *sess {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sess{m: m, id: o.SessionID, log: m.o.Log.With("session", o.SessionID), ctx: ctx, cancel: cancel,
		since: time.Now(), state: stateConnecting, done: make(chan struct{}), fec: &fecControl{}}
	if o.ClientKey != nil {
		s.clientKey = *o.ClientKey
	}
	if o.User != nil {
		s.userID, s.email = o.User.ID, o.User.Email
	}
	for _, is := range o.ICEServers {
		s.iceServers = append(s.iceServers, webrtc.ICEServer{URLs: is.URLs, Username: is.Username, Credential: is.Credential})
	}
	return s
}

func (s *sess) info() *Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &Info{SessionID: s.id, State: s.state, Label: s.clientKey.Label, Email: s.email, Path: s.path, Since: s.since}
}

func (s *sess) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == stateActive
}

func (s *sess) setState(st string) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	s.m.notify(s.info())
}

func (s *sess) send(typ string, msg any) {
	if err := s.m.o.Send(typ, msg); err != nil {
		s.log.Warn("signaling send failed", "type", typ, "err", err)
	}
}

// verifyOffer checks the browser's signature over its own DTLS fingerprint.
func (s *sess) verifyOffer(sdp, sig string) error {
	if s.clientKey.PublicKey == "" {
		return errors.New("offer carries no client key")
	}
	fp, err := proto.SDPFingerprint(sdp)
	if err != nil {
		return err
	}
	return proto.VerifyBrowserSig(s.clientKey.PublicKey, sig, proto.FingerprintMessage(s.id, proto.RoleOffer, fp))
}

func (s *sess) run(o proto.SessionOffer) {
	m := s.m
	if err := s.verifyOffer(o.SDP, o.FPSig); err != nil {
		s.log.Warn("offer signature rejected", "err", err)
		s.end(proto.EndBadSignature, true)
		return
	}
	if m.isRevoked(s.clientKey.PublicKey) {
		s.end(proto.EndNotApproved, true)
		return
	}
	if m.o.Paused() {
		s.end(proto.EndNotApproved, true)
		return
	}
	if p := m.o.Backend.Permissions(); !p.Screen || !p.Input {
		s.log.Warn("missing OS permission", "screen", p.Screen, "input", p.Input)
		s.end(proto.EndPermission, true)
		return
	}

	trusted := m.o.Pins.Trusted(s.clientKey.PublicKey, s.userID)
	remember := !trusted && !m.o.AskNewBrowsers()
	mode := decideConsent(trusted || remember, m.o.AutoAccept())
	if mode != consentAuto {
		s.setState(stateAwaiting)
		s.send(proto.TypeSessionStatus, proto.SessionStatus{SessionID: s.id, Status: proto.StatusAwaitingApproval})
		title, msg := consentText(mode, s.clientKey.Label, s.email, s.clientKey.CreatedAt)
		ctx, cancel := context.WithTimeout(s.ctx, m.o.ConsentTimeout)
		c, err := askConsent(ctx, m.o.Backend, m.o.Prompter, ConsentRequest{
			SessionID: s.id, Title: title, Message: msg, Label: s.clientKey.Label, Email: s.email,
			FirstSeen: s.clientKey.CreatedAt, NewKey: mode == consentNewKey,
		}, int(m.o.ConsentTimeout/time.Second))
		cancel()
		if s.ctx.Err() != nil {
			return // ended while prompting
		}
		if err != nil || c == platform.ConsentDeny {
			s.log.Info("session not approved", "err", err)
			s.end(proto.EndNotApproved, true)
			return
		}
		remember = remember || c == platform.ConsentRemember
	}
	if remember {
		if err := m.o.Pins.Pin(s.clientKey.PublicKey, s.clientKey.Label, s.userID, time.Now()); err != nil {
			s.log.Error("pin failed", "err", err)
		} else if m.o.OnPinsChanged != nil {
			m.o.OnPinsChanged()
		}
	}
	s.send(proto.TypeSessionStatus, proto.SessionStatus{SessionID: s.id, Status: proto.StatusApproved})
	s.setState(stateConnecting)

	if err := s.setupPeer(); err != nil {
		s.log.Error("peer setup failed", "err", err)
		s.end(proto.EndError, true)
		return
	}
	if err := s.answer(o.SDP); err != nil {
		s.log.Error("answer failed", "err", err)
		s.end(proto.EndError, true)
	}
}

func (s *sess) newAPI(estimators chan<- cc.BandwidthEstimator) (*webrtc.API, error) {
	me := &webrtc.MediaEngine{}
	fb := []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"},
		{Type: "nack", Parameter: "pli"}, {Type: "transport-cc"}}
	for i, prof := range h264Profiles {
		if err := me.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
				SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=" + prof, RTCPFeedback: fb},
			PayloadType: webrtc.PayloadType(102 + 2*i),
		}, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000,
			SDPFmtpLine: "profile-id=1", RTCPFeedback: fb},
		PayloadType: fullColorPT,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	reg := &interceptor.Registry{}
	ccf, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(
			gcc.SendSideBWEInitialBitrate(initialKbps*1000),
			gcc.SendSideBWEMinBitrate(floorKbps*1000),
			gcc.SendSideBWEMaxBitrate(maxCeilKbps*1000),
			gcc.SendSideBWEPacer(newFramePacer(initialKbps*1000)),
		)
	})
	if err != nil {
		return nil, err
	}
	ccf.OnNewPeerConnection(func(_ string, e cc.BandwidthEstimator) {
		select {
		case estimators <- e:
		default:
		}
	})
	reg.Add(ccf)
	if err := configureFEC(me, reg, s.fec); err != nil {
		return nil, err
	}
	if err := webrtc.RegisterDefaultInterceptors(me, reg); err != nil {
		return nil, err
	}
	if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI},
		webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	reg.Add(playoutDelayFactory{})

	se := webrtc.SettingEngine{}
	se.SetICETimeouts(4*time.Second, s.m.o.FailedTimeout, time.Second)
	if s.m.udpMux != nil {
		se.SetICEUDPMux(s.m.udpMux)
	}
	if len(s.m.o.NAT1To1IPs) > 0 {
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: s.m.o.NAT1To1IPs, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteAppend,
		}); err != nil {
			return nil, err
		}
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se)), nil
}

func (s *sess) setupPeer() error {
	estimators := make(chan cc.BandwidthEstimator, 1)
	api, err := s.newAPI(estimators)
	if err != nil {
		return err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: s.iceServers})
	if err != nil {
		return err
	}
	track, err := newVideoTrack()
	if err != nil {
		pc.Close()
		return err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return err
	}

	s.in = newInputState(s.m.o.Backend, s.m.o.Keymap)
	s.in.sas = s.m.o.SecureAttention
	s.media = newMediaPipe(s, track)

	s.mu.Lock()
	s.pc = pc
	s.mu.Unlock()

	go func() {
		select {
		case e := <-estimators:
			s.media.attachEstimator(e)
		case <-s.ctx.Done():
		}
	}()
	go s.readRTCP(sender)

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		s.send(proto.TypeICECandidate, proto.ICECandidateMsg{SessionID: s.id, Candidate: &proto.ICECandidate{
			Candidate: init.Candidate, SDPMid: init.SDPMid, SDPMLineIndex: init.SDPMLineIndex, UsernameFragment: init.UsernameFragment,
		}})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.log.Info("peer connection state", "state", st.String())
		switch st {
		case webrtc.PeerConnectionStateConnected:
			go s.onConnected()
		case webrtc.PeerConnectionStateFailed:
			go s.end(proto.EndICEFailed, true)
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "input":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				if !msg.IsString {
					s.onInput(msg.Data)
				}
			})
		case "move":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				if !msg.IsString {
					s.onMove(msg.Data)
				}
			})
		case "cursor":
			s.media.setCursorChannel(dc)
		case "control":
			dc.OnOpen(func() { s.media.setControlChannel(dc) })
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				if msg.IsString {
					s.media.onControlText(msg.Data)
				}
			})
		case "file":
			s.attachFiles(dc)
		default:
			s.log.Warn("unexpected data channel", "label", dc.Label())
			dc.Close()
		}
	})
	return nil
}

// attachFiles serves the `file` channel; without Options.Files (or for a
// second file channel) it is closed, which browsers read as "unsupported".
func (s *sess) attachFiles(dc *webrtc.DataChannel) {
	o := s.m.o
	if o.Files == nil {
		dc.Close()
		return
	}
	svc := files.New(files.Config{Host: o.Files, Policy: files.Policy{Deny: o.FileDeny}, Path: s.currentPath,
		Record: func(r proto.FileRecord) { s.m.audit.record(s.id, r) }, Activity: o.OnFileActivity,
		AuditOK: s.m.audit.ok, Log: s.log})
	s.mu.Lock()
	refuse := s.ended || s.files != nil
	if !refuse {
		s.files = svc
	}
	s.mu.Unlock()
	if refuse {
		svc.Close("refused")
		dc.Close()
		return
	}
	dc.OnOpen(func() { svc.Attach(dc) })
	dc.OnMessage(func(msg webrtc.DataChannelMessage) { svc.Handle(msg.IsString, msg.Data) })
	dc.OnClose(func() { go svc.Close("file channel closed") })
}

func (s *sess) currentPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *sess) readRTCP(sender *webrtc.RTPSender) {
	var last time.Time
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				if time.Since(last) > 100*time.Millisecond {
					last = time.Now()
					s.m.o.Backend.RequestKeyframe()
				}
			}
		}
	}
}

// answer applies a remote offer and sends a signed answer.
func (s *sess) answer(offerSDP string) error {
	s.negMu.Lock()
	defer s.negMu.Unlock()
	s.mu.Lock()
	pc := s.pc
	s.mu.Unlock()
	if pc == nil {
		return errors.New("no peer connection")
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return err
	}
	s.mu.Lock()
	s.remoteSet = true
	pending := s.pendingCands
	s.pendingCands = nil
	s.mu.Unlock()
	for _, c := range pending {
		if err := pc.AddICECandidate(c); err != nil {
			s.log.Debug("buffered candidate rejected", "err", err)
		}
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(ans); err != nil {
		return err
	}
	local := pc.LocalDescription()
	fp, err := proto.SDPFingerprint(local.SDP)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(s.m.o.DeviceKey, proto.FingerprintMessage(s.id, proto.RoleAnswer, fp))
	s.send(proto.TypeSessionAnswer, proto.SessionAnswer{SessionID: s.id, SDP: local.SDP, FPSig: base64.StdEncoding.EncodeToString(sig)})
	return nil
}

// renegotiate handles a second offer on a live session (ICE restart).
func (s *sess) renegotiate(o proto.SessionOffer) {
	if o.ClientKey != nil && o.ClientKey.PublicKey != s.clientKey.PublicKey {
		s.log.Warn("renegotiation offer from a different client key")
		s.end(proto.EndBadSignature, true)
		return
	}
	if err := s.verifyOffer(o.SDP, o.FPSig); err != nil {
		s.log.Warn("renegotiation signature rejected", "err", err)
		s.end(proto.EndBadSignature, true)
		return
	}
	s.mu.Lock()
	ready := s.pc != nil
	s.mu.Unlock()
	if !ready {
		return // still in consent; the browser will retry
	}
	if err := s.answer(o.SDP); err != nil {
		s.log.Error("renegotiation failed", "err", err)
		s.end(proto.EndError, true)
	}
}

func (s *sess) addRemoteCandidate(c *proto.ICECandidate) {
	if c == nil || c.Candidate == "" {
		return
	}
	init := webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: c.SDPMid, SDPMLineIndex: c.SDPMLineIndex, UsernameFragment: c.UsernameFragment}
	s.mu.Lock()
	if !s.remoteSet || s.pc == nil {
		if len(s.pendingCands) < 100 {
			s.pendingCands = append(s.pendingCands, init)
		}
		s.mu.Unlock()
		return
	}
	pc := s.pc
	s.mu.Unlock()
	if err := pc.AddICECandidate(init); err != nil {
		s.log.Debug("remote candidate rejected", "err", err)
	}
}

// PathFor maps the selected candidate pair to the session's ice_path.
func PathFor(local, remote webrtc.ICECandidateType) string {
	switch {
	case local == webrtc.ICECandidateTypeRelay || remote == webrtc.ICECandidateTypeRelay:
		return proto.PathRelay
	case local == webrtc.ICECandidateTypeSrflx || remote == webrtc.ICECandidateTypeSrflx,
		local == webrtc.ICECandidateTypePrflx || remote == webrtc.ICECandidateTypePrflx:
		return proto.PathSrflx
	default:
		return proto.PathHost
	}
}

func (s *sess) onConnected() {
	s.mu.Lock()
	pc := s.pc
	first := !s.started
	s.started = true
	s.mu.Unlock()
	if pc == nil {
		return
	}
	st := proto.SessionState{SessionID: s.id, Connected: true, Path: proto.PathHost}
	if sctp := pc.SCTP(); sctp != nil && sctp.Transport() != nil && sctp.Transport().ICETransport() != nil {
		if pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
			st.Path = PathFor(pair.Local.Typ, pair.Remote.Typ)
			st.LocalCandidate = pair.Local.String()
			st.RemoteCandidate = pair.Remote.String()
		}
	}
	s.mu.Lock()
	s.path = st.Path
	s.mu.Unlock()
	s.setState(stateActive)
	s.send(proto.TypeSessionState, st)
	s.media.setPath(st.Path)
	if first {
		s.m.o.Backend.Indicator(true)
		s.media.start()
	}
}

func (s *sess) onInput(b []byte) { s.checkAbuse(s.in.handle(b, s.media)) }

func (s *sess) onMove(b []byte) { s.checkAbuse(s.in.handleMove(b, s.media)) }

func (s *sess) checkAbuse(err error) {
	if err != nil && s.in.rejects() > maxRejects {
		s.log.Warn("input abuse threshold reached")
		go s.end(proto.EndAbuse, true)
	}
}

// end tears the session down once. send=true reports session.end (with
// byte counters) to the server.
func (s *sess) end(reason string, send bool) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		<-s.done
		return
	}
	s.ended = true
	s.state = stateEnded
	pc := s.pc
	started := s.started
	fsvc := s.files
	s.mu.Unlock()
	s.cancel()

	// Transfers end first so their `cancelled` records reach the server
	// before session.end.
	if fsvc != nil {
		fsvc.Close("session ended")
	}
	if s.m.audit != nil {
		s.m.audit.flush(s.id)
	}
	var sent, recv int64
	if s.media != nil {
		s.media.stop(reason)
	}
	if s.in != nil {
		s.in.releaseAll()
	}
	if started {
		s.m.o.Backend.Indicator(false)
	}
	if pc != nil {
		sent, recv = transportBytes(pc)
		_ = pc.Close()
	}
	if send {
		s.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: s.id, Reason: reason, BytesSent: sent, BytesRecv: recv})
	}
	s.log.Info("session ended", "reason", reason, "bytes_sent", sent, "bytes_recv", recv)
	close(s.done)
	s.m.release(s)
}

func transportBytes(pc *webrtc.PeerConnection) (sent, recv int64) {
	for _, st := range pc.GetStats() {
		if ts, ok := st.(webrtc.TransportStats); ok {
			sent += int64(ts.BytesSent)
			recv += int64(ts.BytesReceived)
		}
	}
	return sent, recv
}
