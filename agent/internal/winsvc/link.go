package winsvc

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/ruolez/remotectl/agent/internal/session"
	"github.com/ruolez/remotectl/proto"
)

// HelperTarget is what the helper drives: the real *session.Manager.
type HelperTarget interface {
	session.Handler
	SetRevoked(spkis []string)
	Revoke(spki string)
}

// Link is the helper side of the pipe pair. Send implements
// session.Outbound, so it is handed to session.Manager as Options.Send.
type Link struct {
	mu sync.Mutex
	w  io.Writer
}

func NewLink(w io.Writer) *Link { return &Link{w: w} }

func (l *Link) write(kind, typ string, v any) error {
	f, err := newFrame(kind, typ, v)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return WriteFrame(l.w, f)
}

// Send implements session.Outbound.
func (l *Link) Send(msgType string, msg any) error { return l.write(KindOut, msgType, msg) }

func (l *Link) Caps(c HelperCaps) error   { return l.write(KindCaps, "", c) }
func (l *Link) Active(ids []string) error { return l.write(KindActive, "", ids) }
func (l *Link) Quit() error               { return l.write(KindQuit, "", nil) }
func (l *Link) SAS() error                { return l.write(KindSAS, "", nil) }

var _ session.Outbound = (*Link)(nil).Send

// HelperEvents receive service → helper notifications other than signaling.
type HelperEvents struct {
	OnDevice func(proto.DeviceInfo)
	OnStatus func(ServiceStatus)
}

// Serve reads service frames until r fails or a shutdown frame arrives
// (returns nil). Signaling goes to target; it never blocks on the target
// for longer than the target's own HandleSignal does.
func Serve(r io.Reader, target HelperTarget, ev HelperEvents) error {
	for {
		f, err := ReadFrame(r)
		if err != nil {
			return err
		}
		switch f.Kind {
		case KindSignal:
			if f.Type != "" {
				target.HandleSignal(f.Type, f.Data)
			}
		case KindEndAll:
			var reason string
			if json.Unmarshal(f.Data, &reason) != nil || reason == "" {
				reason = proto.EndAgentKill
			}
			target.EndAll(reason)
		case KindRevokedSet:
			var spkis []string
			if json.Unmarshal(f.Data, &spkis) == nil {
				target.SetRevoked(spkis)
			}
		case KindRevokedAdd:
			var spki string
			if json.Unmarshal(f.Data, &spki) == nil && spki != "" {
				target.Revoke(spki)
			}
		case KindDevice:
			var d proto.DeviceInfo
			if json.Unmarshal(f.Data, &d) == nil && ev.OnDevice != nil {
				ev.OnDevice(d)
			}
		case KindStatus:
			var s ServiceStatus
			if json.Unmarshal(f.Data, &s) == nil && ev.OnStatus != nil {
				ev.OnStatus(s)
			}
		case KindShutdown:
			return nil
		}
	}
}
