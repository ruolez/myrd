// Package session runs the agent side of a remote-control session: consent,
// DTLS fingerprint signing/verification, the pion peer connection, the video
// track fed by the native encoder, and the input/cursor/control channels.
package session

import (
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/agent/internal/keymap"
	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

// Options configure a Manager. Backend, DeviceKey, Pins and Send are required.
type Options struct {
	Backend   platform.Backend
	DeviceKey ed25519.PrivateKey
	Pins      *pins.Store
	Send      Outbound
	// Prompter is used when Backend.ConsentPrompt returns ErrUnsupported.
	Prompter Prompter

	AutoAccept func() bool // device auto_accept from the server (hello_ack / device.update)
	Paused     func() bool // tray/console "Pause": refuse new sessions

	OnPinsChanged   func()      // send pins.sync
	OnSessionChange func(*Info) // nil = no session; for tray + device console

	// Network: UDPPort > 0 multiplexes ICE on one UDP port; NAT1To1IPs adds
	// extra host candidates with those IPs (Docker Desktop: 127.0.0.1).
	UDPPort    int
	NAT1To1IPs []string

	AgentVersion string
	MaxFPS       int // 0 = backend default (60, 30 for software x264)
	MaxWidth     int // 0 = native
	MaxHeight    int

	// Files enables the `file` channel (transfers and browsing as the
	// device's user); nil disables it and the channel is closed. FileDeny
	// lists paths it never touches (the agent's state directory).
	// OnFileActivity feeds the tray / device console and must not block.
	Files          files.Host
	FileDeny       []string
	OnFileActivity func(files.Activity)

	ConsentTimeout time.Duration // default 60 s
	FailedTimeout  time.Duration // ICE failed → end; default 15 s
	Keymap         keymap.Table  // default keymap.For(runtime.GOOS)
	Log            *slog.Logger
}

// Info describes the current session for local UI.
type Info struct {
	SessionID string
	State     string // "awaiting_approval" | "connecting" | "active"
	Label     string
	Email     string
	Path      string
	Since     time.Time
}

// Manager implements Handler. One session at a time.
type Manager struct {
	o      Options
	udpMux ice.UDPMux

	audit *fileAudit // nil without Files

	mu      sync.Mutex
	cur     *sess
	revoked map[string]bool // SPKI b64
}

var _ Handler = (*Manager)(nil)

func NewManager(o Options) (*Manager, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ConsentTimeout == 0 {
		o.ConsentTimeout = 60 * time.Second
	}
	if o.FailedTimeout == 0 {
		o.FailedTimeout = 15 * time.Second
	}
	if o.Keymap == nil {
		o.Keymap = keymap.For(runtime.GOOS)
	}
	if o.AutoAccept == nil {
		o.AutoAccept = func() bool { return false }
	}
	if o.Paused == nil {
		o.Paused = func() bool { return false }
	}
	m := &Manager{o: o, revoked: map[string]bool{}}
	if o.Files != nil {
		m.audit = newFileAudit(o.Send, o.Log)
		go m.audit.run()
	}
	if o.UDPPort > 0 {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: o.UDPPort})
		if err != nil {
			return nil, err
		}
		m.udpMux = webrtc.NewICEUDPMux(nil, conn)
	}
	return m, nil
}

// SetRevoked replaces the revoked client-key set (from hello_ack) and drops
// those keys from the local pins.
func (m *Manager) SetRevoked(spkis []string) {
	m.mu.Lock()
	m.revoked = map[string]bool{}
	for _, k := range spkis {
		m.revoked[k] = true
	}
	m.mu.Unlock()
	if n, _ := m.o.Pins.Revoke(spkis); n > 0 && m.o.OnPinsChanged != nil {
		m.o.OnPinsChanged()
	}
}

// Revoke adds one key (client_key.revoked) and ends a session using it.
func (m *Manager) Revoke(spki string) {
	m.mu.Lock()
	m.revoked[spki] = true
	cur := m.cur
	m.mu.Unlock()
	if n, _ := m.o.Pins.Revoke([]string{spki}); n > 0 && m.o.OnPinsChanged != nil {
		m.o.OnPinsChanged()
	}
	if cur != nil && cur.clientKey.PublicKey == spki {
		cur.end(proto.EndNotApproved, true)
	}
}

func (m *Manager) isRevoked(spki string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revoked[spki]
}

func (m *Manager) HandleSignal(msgType string, raw []byte) {
	switch msgType {
	case proto.TypeSessionOffer:
		var o proto.SessionOffer
		if err := json.Unmarshal(raw, &o); err != nil || o.SessionID == "" {
			m.o.Log.Warn("bad session.offer")
			return
		}
		m.handleOffer(o)
	case proto.TypeICECandidate:
		var c proto.ICECandidateMsg
		if err := json.Unmarshal(raw, &c); err != nil {
			return
		}
		if s := m.get(c.SessionID); s != nil {
			s.addRemoteCandidate(c.Candidate)
		}
	case proto.TypeSessionEnd:
		var e proto.SessionEnd
		if err := json.Unmarshal(raw, &e); err != nil {
			return
		}
		if s := m.get(e.SessionID); s != nil {
			// Report our byte counters back; the server treats a second end as an update.
			s.end(e.Reason, true)
		}
	}
}

func (m *Manager) get(id string) *sess {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.id == id {
		return m.cur
	}
	return nil
}

func (m *Manager) handleOffer(o proto.SessionOffer) {
	m.mu.Lock()
	cur := m.cur
	if cur != nil && cur.id == o.SessionID {
		m.mu.Unlock()
		go cur.renegotiate(o)
		return
	}
	m.mu.Unlock()
	if cur != nil {
		// The server only offers when it believes the device is free, so the
		// old session is stale on its side.
		cur.end(proto.EndPeerGone, true)
	}
	s := newSess(m, o)
	m.mu.Lock()
	m.cur = s
	m.mu.Unlock()
	m.notify(s.info())
	go s.run(o)
}

func (m *Manager) release(s *sess) {
	m.mu.Lock()
	if m.cur == s {
		m.cur = nil
	}
	m.mu.Unlock()
	m.notify(nil)
}

func (m *Manager) notify(i *Info) {
	if m.o.OnSessionChange != nil {
		m.o.OnSessionChange(i)
	}
}

func (m *Manager) ActiveSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.isConnected() {
		return []string{m.cur.id}
	}
	return []string{}
}

func (m *Manager) EndAll(reason string) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur != nil {
		cur.end(reason, true)
	}
}

// Current returns the current session info, or nil.
func (m *Manager) Current() *Info {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	if cur == nil {
		return nil
	}
	return cur.info()
}

// Close ends any session, makes a last attempt at unsent file audit records
// and releases the UDP port.
func (m *Manager) Close() {
	m.EndAll(proto.EndAgentKill)
	if m.audit != nil {
		m.audit.close()
	}
	if m.udpMux != nil {
		_ = m.udpMux.Close()
	}
}
