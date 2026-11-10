package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

type deviceSessionResp struct {
	Session *store.Session `json:"session"`
}

// connectedSession opens a session from browser and lets the agent report it connected.
func connectedSession(t *testing.T, browser, agent *wsClient, deviceID, keyID string) string {
	t.Helper()
	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "hold", DeviceID: deviceID, ClientKeyID: keyID})
	var created proto.SessionCreated
	browser.expect(proto.TypeSessionCreated, &created)
	browser.send(proto.TypeSessionOffer, proto.SessionOffer{SessionID: created.SessionID, SDP: "v=0 offer", FPSig: "sig"})
	agent.expect(proto.TypeSessionOffer, nil)
	agent.send(proto.TypeSessionState, proto.SessionState{SessionID: created.SessionID, Connected: true, Path: proto.PathHost})
	browser.expect(proto.TypeSessionState, nil)
	return created.SessionID
}

func TestTakeOverBusyDevice(t *testing.T) {
	e := newEnv(t)
	first := e.login(adminEmail, adminPass)
	second := e.login(adminEmail, adminPass)
	ag := e.enroll(first)
	firstKey := e.registerClientKey(first)
	secondKey := e.registerClientKey(second)

	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{AgentVersion: "0.1.0", OS: "darwin", Arch: "arm64", ActiveSessions: []string{}})
	agent.expect(proto.TypeHelloAck, nil)
	b1 := e.dialBrowser(first)
	b2 := e.dialBrowser(second)
	held := connectedSession(t, b1, agent, ag.id, firstKey)

	path := "/v1/devices/" + ag.id + "/session"
	var got deviceSessionResp
	if st := e.call(second, "GET", path, nil, &got); st != 200 || got.Session == nil {
		t.Fatalf("GET holder: %d %+v", st, got)
	}
	if h := got.Session; h.ID != held || h.State != "active" || h.Mode != proto.ModeDesktop ||
		h.ClientKeyID == nil || *h.ClientKeyID != firstKey || h.ClientKeyLabel == nil || h.ConnectedAt == nil {
		t.Fatalf("holder = %+v", h)
	}

	b2.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "busy", DeviceID: ag.id, ClientKeyID: secondKey})
	var busy proto.Error
	b2.expect(proto.TypeError, &busy)
	if busy.Code != proto.ErrDeviceBusy {
		t.Fatalf("create on a busy device: %+v", busy)
	}

	var ended struct{ Ended int }
	if st := e.call(second, "POST", path+"/end", map[string]any{"takeover": true}, &ended); st != 200 || ended.Ended != 1 {
		t.Fatalf("take over: %d %+v", st, ended)
	}
	var end proto.SessionEnd
	b1.expect(proto.TypeSessionEnd, &end)
	if end.SessionID != held || end.Reason != proto.EndTakenOver {
		t.Fatalf("holder's browser got %+v", end)
	}
	agent.expect(proto.TypeSessionEnd, &end)
	if end.SessionID != held || end.Reason != proto.EndTakenOver {
		t.Fatalf("agent got %+v", end)
	}
	if got := strings.Join(e.auditEvents(held), ","); got != "session.created,session.connected,session.taken_over,session.ended" {
		t.Fatalf("audit = %s", got)
	}

	got = deviceSessionResp{}
	if st := e.call(second, "GET", path, nil, &got); st != 200 || got.Session != nil {
		t.Fatalf("GET after take over: %d %+v", st, got.Session)
	}
	b2.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "mine", DeviceID: ag.id, ClientKeyID: secondKey})
	var created proto.SessionCreated
	b2.expect(proto.TypeSessionCreated, &created)
	if created.Req != "mine" {
		t.Fatalf("create after take over: %+v", created)
	}
}

func TestEndDeviceSessionWithoutTakingOver(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	keyID := e.registerClientKey(c)
	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{AgentVersion: "0.1.0", OS: "darwin", Arch: "arm64", ActiveSessions: []string{}})
	agent.expect(proto.TypeHelloAck, nil)
	browser := e.dialBrowser(c)

	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r1", DeviceID: ag.id, ClientKeyID: keyID, Mode: proto.ModeTerminal})
	var created proto.SessionCreated
	browser.expect(proto.TypeSessionCreated, &created)

	path := "/v1/devices/" + ag.id + "/session"
	var got deviceSessionResp
	if st := e.call(c, "GET", path, nil, &got); st != 200 || got.Session == nil ||
		got.Session.State != "pending" || got.Session.Mode != proto.ModeTerminal {
		t.Fatalf("GET pending holder: %d %+v", st, got.Session)
	}
	var ended struct{ Ended int }
	if st := e.call(c, "POST", path+"/end", map[string]any{}, &ended); st != 200 || ended.Ended != 1 {
		t.Fatalf("end: %d %+v", st, ended)
	}
	var end proto.SessionEnd
	browser.expect(proto.TypeSessionEnd, &end)
	if end.SessionID != created.SessionID || end.Reason != proto.EndEndedRemotely {
		t.Fatalf("browser got %+v", end)
	}
	var state, reason string
	_ = e.pool.QueryRow(context.Background(), `SELECT state, end_reason FROM sessions WHERE id = $1`, created.SessionID).Scan(&state, &reason)
	if state != "failed" || reason != proto.EndEndedRemotely {
		t.Fatalf("db = %s %s", state, reason)
	}
	if got := strings.Join(e.auditEvents(created.SessionID), ","); got != "session.created,session.terminated,session.failed" {
		t.Fatalf("audit = %s", got)
	}

	if st := e.call(c, "POST", path+"/end", map[string]any{"takeover": true}, &ended); st != 200 || ended.Ended != 0 {
		t.Fatalf("end on a free device: %d %+v", st, ended)
	}
}

func TestDeviceSessionIsOwnerOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	ag := e.enroll(admin)
	bob := e.createUser(admin, "bob@example.com")
	path := "/v1/devices/" + ag.id + "/session"
	if st := e.call(bob, "GET", path, nil, nil); st != 404 {
		t.Fatalf("bob GET = %d", st)
	}
	if st := e.call(bob, "POST", path+"/end", map[string]any{"takeover": true}, nil); st != 404 {
		t.Fatalf("bob POST end = %d", st)
	}
}
