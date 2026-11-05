package app

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// A terminal session is created and relayed with its mode, the agent learns
// whether terminals are allowed (hello_ack, then device.update when an admin
// changes it), terminal audit records land in the log, and an organization
// that turned terminals off refuses terminal sessions.
func TestTerminalSessionsAndAudit(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	keyID := e.registerClientKey(c)
	browser := e.dialBrowser(c)
	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{OS: "darwin", Arch: "arm64", ActiveSessions: []string{}})
	var ack proto.HelloAck
	agent.expect(proto.TypeHelloAck, &ack)
	if !slices.Contains(ack.Features, proto.FeatureTerminal) {
		t.Fatalf("hello_ack features %v lack terminal", ack.Features)
	}

	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r1", DeviceID: ag.id, ClientKeyID: keyID, Mode: proto.ModeTerminal})
	var created proto.SessionCreated
	browser.expect(proto.TypeSessionCreated, &created)
	sid := created.SessionID
	browser.send(proto.TypeSessionOffer, proto.SessionOffer{SessionID: sid, SDP: "v=0", FPSig: "sig"})
	var offer proto.SessionOffer
	agent.expect(proto.TypeSessionOffer, &offer)
	if offer.Mode != proto.ModeTerminal {
		t.Fatalf("offer relayed with mode %q", offer.Mode)
	}
	var mode string
	_ = e.pool.QueryRow(context.Background(), `SELECT mode FROM sessions WHERE id = $1`, sid).Scan(&mode)
	if mode != proto.ModeTerminal {
		t.Fatalf("stored mode %q", mode)
	}

	agent.send(proto.TypeSessionState, proto.SessionState{SessionID: sid, Connected: true, Path: proto.PathHost})
	browser.expect(proto.TypeSessionState, nil)
	code := 0
	agent.send(proto.TypeSessionTerminal, proto.SessionTerminal{SessionID: sid, Terminals: []proto.TerminalRecord{
		{Term: 1, Action: proto.TermOpened, Source: proto.TermSourceShell, Title: "zsh", Result: "ok"},
		{Term: 1, Action: "typed", Source: proto.TermSourceShell, Result: "ok"}, // invalid: dropped
		{Term: 1, Action: proto.TermClosed, Source: proto.TermSourceShell, Title: "zsh", Result: "ok", Reason: "exited",
			ExitCode: &code, BytesIn: 12, BytesOut: 3400, DurationMS: 9000},
	}})
	want := "terminal.opened,terminal.closed"
	terminalEvents := func() string {
		var out []string
		for _, ev := range e.auditEvents(sid) {
			if strings.HasPrefix(ev, "terminal.") {
				out = append(out, ev)
			}
		}
		return strings.Join(out, ",")
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && terminalEvents() != want; time.Sleep(20 * time.Millisecond) {
	}
	if got := terminalEvents(); got != want {
		t.Fatalf("terminal audit events %q, want %q", got, want)
	}
	agent.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sid, Reason: proto.EndUserHangup})
	browser.expect(proto.TypeSessionEnd, nil)

	// Turning terminals off reaches the connected agent and refuses new ones.
	var s map[string]any
	if st := e.call(c, http.MethodGet, "/v1/admin/settings", nil, &s); st != http.StatusOK {
		t.Fatalf("get settings: %d", st)
	}
	s["terminal_allowed"] = false
	if st := e.call(c, http.MethodPut, "/v1/admin/settings", s, &s); st != http.StatusOK || s["terminal_allowed"] != false {
		t.Fatalf("put settings: %d %v", st, s)
	}
	var upd proto.DeviceUpdate
	agent.expect(proto.TypeDeviceUpdate, &upd)
	if upd.Terminal == nil || *upd.Terminal {
		t.Fatalf("device.update terminal %v, want false", upd.Terminal)
	}
	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r2", DeviceID: ag.id, ClientKeyID: keyID, Mode: proto.ModeTerminal})
	var er proto.Error
	browser.expect(proto.TypeError, &er)
	if er.Code != proto.ErrTerminalDisabled || er.Req != "r2" {
		t.Fatalf("error %+v, want %s", er, proto.ErrTerminalDisabled)
	}
	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r3", DeviceID: ag.id, ClientKeyID: keyID, Mode: "ssh"})
	browser.expect(proto.TypeError, &er)
	if er.Code != proto.ErrBadRequest {
		t.Fatalf("unknown mode: error %+v", er)
	}
	// Desktop sessions are unaffected.
	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r4", DeviceID: ag.id, ClientKeyID: keyID})
	browser.expect(proto.TypeSessionCreated, &created)

	agent2 := e.dialAgent(e.enroll(c))
	agent2.send(proto.TypeHello, proto.Hello{OS: "linux", Arch: "amd64", ActiveSessions: []string{}})
	agent2.expect(proto.TypeHelloAck, &ack)
	if slices.Contains(ack.Features, proto.FeatureTerminal) {
		t.Fatalf("hello_ack features %v offer terminal while turned off", ack.Features)
	}
}
