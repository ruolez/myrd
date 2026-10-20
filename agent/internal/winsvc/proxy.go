package winsvc

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruolez/remotectl/agent/internal/session"
	"github.com/ruolez/remotectl/proto"
)

// Proxy is the service-side session.Handler: it forwards signaling to the
// helper in the console session and relays the helper's outbound messages to
// the server. State the helper needs (revoked keys, device settings, tray
// status) is remembered and replayed whenever a helper attaches.
type Proxy struct {
	Send   session.Outbound // towards the server
	Log    *slog.Logger
	OnCaps func(HelperCaps)
	OnQuit func()

	mu      sync.Mutex
	w       io.Writer
	active  []string
	revoked []string
	device  *proto.DeviceInfo
	status  *ServiceStatus
	caps    *HelperCaps
	// deviceKey is the identity the service signs in with; helperKey is the
	// one the attached helper reported (empty until its caps arrive).
	deviceKey string
	helperKey string

	held atomic.Bool // an installer is replacing the exe: no helper may run
}

var _ session.Handler = (*Proxy)(nil)

func (p *Proxy) log() *slog.Logger {
	if p.Log == nil {
		return slog.Default()
	}
	return p.Log
}

// write sends a frame to the helper; false when no helper is attached.
func (p *Proxy) write(kind, typ string, v any) bool {
	f, err := newFrame(kind, typ, v)
	if err != nil {
		p.log().Warn("winsvc: encode frame", "kind", kind, "err", err)
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w == nil {
		return false
	}
	if err := WriteFrame(p.w, f); err != nil {
		p.log().Warn("winsvc: write to helper", "kind", kind, "err", err)
		return false
	}
	return true
}

// Attach serves one helper connection until r fails, then detaches. Sessions
// the helper had open are reported ended so nothing waits on a dead process.
func (p *Proxy) Attach(r io.Reader, w io.Writer) error {
	p.mu.Lock()
	p.w = w
	p.helperKey = ""
	replay := []Frame{}
	if p.revoked != nil {
		f, _ := newFrame(KindRevokedSet, "", p.revoked)
		replay = append(replay, f)
	}
	if p.device != nil {
		f, _ := newFrame(KindDevice, "", p.device)
		replay = append(replay, f)
	}
	if p.status != nil {
		f, _ := newFrame(KindStatus, "", p.status)
		replay = append(replay, f)
	}
	for _, f := range replay {
		if err := WriteFrame(w, f); err != nil {
			p.w = nil
			p.mu.Unlock()
			return err
		}
	}
	p.mu.Unlock()

	err := p.readLoop(r)

	p.mu.Lock()
	p.w = nil
	orphans := p.active
	p.active = nil
	p.mu.Unlock()
	for _, id := range orphans {
		p.sendServer(proto.TypeSessionEnd, proto.SessionEnd{SessionID: id, Reason: proto.EndError})
	}
	return err
}

func (p *Proxy) readLoop(r io.Reader) error {
	for {
		f, err := ReadFrame(r)
		if err != nil {
			return err
		}
		switch f.Kind {
		case KindOut:
			if f.Type == "" || len(f.Data) == 0 {
				continue
			}
			p.sendServer(f.Type, f.Data)
		case KindActive:
			var ids []string
			if json.Unmarshal(f.Data, &ids) == nil {
				p.mu.Lock()
				p.active = ids
				p.mu.Unlock()
			}
		case KindCaps:
			var c HelperCaps
			if json.Unmarshal(f.Data, &c) == nil {
				p.mu.Lock()
				p.helperKey = c.DeviceKey
				stale := p.staleHelperLocked()
				p.mu.Unlock()
				if stale {
					p.restartStaleHelper()
				}
				p.mu.Lock()
				p.caps = &c
				p.mu.Unlock()
				if p.OnCaps != nil {
					p.OnCaps(c)
				}
			}
		case KindQuit:
			if p.OnQuit != nil {
				p.OnQuit()
			}
		default:
			p.log().Debug("winsvc: unknown frame from helper", "kind", f.Kind)
		}
	}
}

func (p *Proxy) sendServer(typ string, msg any) {
	if p.Send == nil {
		return
	}
	if err := p.Send(typ, msg); err != nil {
		p.log().Warn("winsvc: send to server", "type", typ, "err", err)
	}
}

// HandleSignal implements session.Handler.
func (p *Proxy) HandleSignal(msgType string, raw []byte) {
	if p.write(KindSignal, msgType, json.RawMessage(raw)) {
		return
	}
	// No helper (nobody at the console yet, or it is restarting): refuse an
	// offer at once instead of letting the browser wait for the timeout.
	if msgType == proto.TypeSessionOffer {
		var o struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(raw, &o) == nil && o.SessionID != "" {
			p.sendServer(proto.TypeSessionEnd, proto.SessionEnd{SessionID: o.SessionID, Reason: proto.EndError})
		}
	}
}

// ActiveSessions implements session.Handler from the helper's last report.
func (p *Proxy) ActiveSessions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.active...)
}

// EndAll implements session.Handler.
func (p *Proxy) EndAll(reason string) { p.write(KindEndAll, "", reason) }

// SetRevoked forwards the revoked client-key set from hello_ack.
func (p *Proxy) SetRevoked(spkis []string) {
	p.mu.Lock()
	p.revoked = append([]string{}, spkis...)
	p.mu.Unlock()
	p.write(KindRevokedSet, "", spkis)
}

// Revoke forwards one client_key.revoked.
func (p *Proxy) Revoke(spki string) {
	p.mu.Lock()
	p.revoked = append(p.revoked, spki)
	p.mu.Unlock()
	p.write(KindRevokedAdd, "", spki)
}

// SetDevice forwards device settings (auto_accept) from hello_ack / device.update.
func (p *Proxy) SetDevice(d proto.DeviceInfo) {
	p.mu.Lock()
	p.device = &d
	p.mu.Unlock()
	p.write(KindDevice, "", d)
}

// SetStatus forwards the connection status for the helper's tray; the
// update line set by SetUpdateLine is kept.
func (p *Proxy) SetStatus(s ServiceStatus) {
	p.mu.Lock()
	if p.status != nil {
		s.Update = p.status.Update
	}
	p.status = &s
	p.mu.Unlock()
	p.write(KindStatus, "", s)
}

// SetUpdateLine shows update progress in the helper's tray.
func (p *Proxy) SetUpdateLine(line string) {
	p.mu.Lock()
	s := ServiceStatus{}
	if p.status != nil {
		s = *p.status
	}
	s.Update = line
	p.status = &s
	p.mu.Unlock()
	p.write(KindStatus, "", s)
}

// Caps returns the helper's last capability report, if any.
func (p *Proxy) Caps() (HelperCaps, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.caps == nil {
		return HelperCaps{}, false
	}
	return *p.caps, true
}

// SetDeviceKey records the device public key (base64) the service is
// enrolled with. A helper that loaded an older key, e.g. before the device
// was removed and enrolled again, would sign session answers browsers
// reject, so it is asked to exit; the supervisor starts a fresh one.
func (p *Proxy) SetDeviceKey(pub string) {
	p.mu.Lock()
	p.deviceKey = pub
	stale := p.staleHelperLocked()
	p.mu.Unlock()
	if stale {
		p.restartStaleHelper()
	}
}

func (p *Proxy) staleHelperLocked() bool {
	return p.w != nil && p.helperKey != "" && p.deviceKey != "" && p.helperKey != p.deviceKey
}

func (p *Proxy) restartStaleHelper() {
	p.log().Warn("winsvc: helper holds a previous device key; restarting it")
	p.Shutdown()
}

// HoldHelper stops the helper and keeps the supervisor from starting another
// until release is called: msiexec cannot replace the exe while a helper runs
// it. It waits up to wait for the helper to disconnect.
func (p *Proxy) HoldHelper(wait time.Duration) (release func()) {
	p.held.Store(true)
	p.Shutdown()
	for deadline := time.Now().Add(wait); p.attached() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	return func() { p.held.Store(false) }
}

// Held reports whether HoldHelper is in effect.
func (p *Proxy) Held() bool { return p.held.Load() }

func (p *Proxy) attached() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w != nil
}

// Shutdown asks an attached helper to exit.
func (p *Proxy) Shutdown() bool { return p.write(KindShutdown, "", nil) }
