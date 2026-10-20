package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

type apiErr struct {
	Error struct{ Code string }
}

// online dials the agent socket and says hello with version on os.
func (e *env) online(ag *fakeAgent, os, version string) *wsClient {
	e.t.Helper()
	w := e.dialAgent(ag)
	w.send(proto.TypeHello, proto.Hello{AgentVersion: version, OS: os, Arch: "arm64", ActiveSessions: []string{}})
	w.expect(proto.TypeHelloAck, nil)
	return w
}

func (e *env) device(c *http.Client, id string) store.Device {
	e.t.Helper()
	var out struct{ Device store.Device }
	if st := e.call(c, "GET", "/v1/devices/"+id, nil, &out); st != 200 {
		e.t.Fatalf("get device: %d", st)
	}
	return out.Device
}

func expectUpdateStatus(t *testing.T, w *wsClient, deviceID string, want proto.UpdateStatus) {
	t.Helper()
	var got proto.DeviceUpdateStatus
	w.expect(proto.TypeDeviceUpdateStatus, &got)
	if at, err := time.Parse(time.RFC3339, got.At); err != nil || time.Since(at) > time.Minute {
		t.Fatalf("device.update_status at %q", got.At)
	}
	want2 := proto.DeviceUpdateStatus{V: proto.Version, Type: proto.TypeDeviceUpdateStatus, DeviceID: deviceID,
		State: want.State, From: want.From, Version: want.Version, Error: want.Error, At: got.At}
	if got != want2 {
		t.Fatalf("device.update_status\n got %+v\nwant %+v", got, want2)
	}
}

func TestUpdateNowAndStatus(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	ag := e.enroll(admin)
	e.release(admin, "0.2.0", "darwin", "universal", 0, true)
	browser := e.dialBrowser(admin)
	agent := e.online(ag, "darwin", "0.1.0")

	var acc struct{ Version string }
	if st := e.call(admin, "POST", "/v1/devices/"+ag.id+"/update", map[string]any{"force": true}, &acc); st != 202 || acc.Version != "0.2.0" {
		t.Fatalf("update now: %d %+v", st, acc)
	}
	var check proto.UpdateCheck
	agent.expect(proto.TypeUpdateCheck, &check)
	if check != (proto.UpdateCheck{V: proto.Version, Type: proto.TypeUpdateCheck, Explicit: true, Force: true}) {
		t.Fatalf("update.check %+v", check)
	}
	expectUpdateStatus(t, browser, ag.id, proto.UpdateStatus{State: proto.UpdateChecking, From: "0.1.0", Version: "0.2.0"})
	var meta map[string]any
	var actor string
	_ = e.pool.QueryRow(context.Background(), `SELECT actor, meta FROM audit_events WHERE event = 'agent.update.requested'`).Scan(&actor, &meta)
	if actor != "user" || meta["version"] != "0.2.0" || meta["force"] != true {
		t.Fatalf("agent.update.requested %s %v", actor, meta)
	}

	downloading := proto.UpdateStatus{State: proto.UpdateDownloading, From: "0.1.0", Version: "0.2.0"}
	agent.send(proto.TypeUpdateStatus, downloading)
	expectUpdateStatus(t, browser, ag.id, downloading)
	agent.send(proto.TypeUpdateStatus, downloading) // unchanged: not relayed again
	installing := proto.UpdateStatus{State: proto.UpdateInstalling, From: "0.1.0", Version: "0.2.0"}
	agent.send(proto.TypeUpdateStatus, installing)
	expectUpdateStatus(t, browser, ag.id, installing)
	agent.send(proto.TypeUpdateStatus, proto.UpdateStatus{State: "bogus"})
	var er proto.Error
	agent.expect(proto.TypeError, &er)
	if er.Code != proto.ErrBadRequest {
		t.Fatalf("bogus state: %+v", er)
	}
	if n := e.eventCount("agent.update.started"); n != 1 {
		t.Fatalf("agent.update.started audited %d times", n)
	}

	// The new build's hello completes the update.
	agent.c.CloseNow()
	agent = e.online(ag, "darwin", "0.2.0")
	expectUpdateStatus(t, browser, ag.id, proto.UpdateStatus{State: proto.UpdateUpToDate, From: "0.1.0", Version: "0.2.0"})
	if n := e.eventCount("agent.update.succeeded"); n != 1 {
		t.Fatalf("agent.update.succeeded audited %d times", n)
	}
	d := e.device(admin, ag.id)
	if d.UpdateState != proto.UpdateUpToDate || d.AgentVersion != "0.2.0" || d.UpdateAvailable || d.UpdateAt == nil {
		t.Fatalf("device after update %+v", d)
	}
	var conflict apiErr
	if st := e.call(admin, "POST", "/v1/devices/"+ag.id+"/update", nil, &conflict); st != 409 || conflict.Error.Code != "up_to_date" {
		t.Fatalf("update when current: %d %+v", st, conflict)
	}

	// An install still on the old version after 15 minutes failed.
	if _, err := e.pool.Exec(context.Background(), `UPDATE devices SET update_state = 'installing', update_from = '0.2.0',
		update_version = '0.3.0', update_at = now() - interval '16 minutes' WHERE id = $1`, ag.id); err != nil {
		t.Fatal(err)
	}
	agent.c.CloseNow()
	agent = e.online(ag, "darwin", "0.2.0")
	expectUpdateStatus(t, browser, ag.id, proto.UpdateStatus{State: proto.UpdateFailed, From: "0.2.0", Version: "0.3.0",
		Error: "still on 0.2.0 after install"})

	// Reported failures are truncated to valid UTF-8 and audited.
	long := strings.Repeat("é", proto.MaxUpdateErrorLen)
	agent.send(proto.TypeUpdateStatus, proto.UpdateStatus{State: proto.UpdateRolledBack, From: "0.3.0", Version: "0.3.0", Error: long})
	var got proto.DeviceUpdateStatus
	browser.expect(proto.TypeDeviceUpdateStatus, &got)
	if got.State != proto.UpdateRolledBack || len(got.Error) != proto.MaxUpdateErrorLen || !utf8.ValidString(got.Error) {
		t.Fatalf("rolled_back relay: state %s error %d bytes", got.State, len(got.Error))
	}
	if d := e.device(admin, ag.id); d.UpdateError != got.Error {
		t.Fatalf("stored error differs from the relayed one")
	}
	if f, r := e.eventCount("agent.update.failed"), e.eventCount("agent.update.rolled_back"); f != 1 || r != 1 {
		t.Fatalf("failed audited %d, rolled_back %d", f, r)
	}
}

func TestUpdateNowRefusals(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	user := e.createUser(admin, "user@example.com")
	mine := e.enroll(user)
	linux := e.enroll(user)
	e.release(admin, "0.2.0", "darwin", "universal", 100, true)
	e.online(linux, "linux", "0.1.0")

	cases := []struct {
		name   string
		client *http.Client
		path   string
		status int
		code   string
	}{
		{"offline", user, "/v1/devices/" + mine.id + "/update", 409, "device_offline"},
		{"linux demo device", user, "/v1/devices/" + linux.id + "/update", 409, "unsupported"},
		{"someone else's device", admin, "/v1/devices/" + mine.id + "/update", 404, "not_found"},
		{"admin route, offline", admin, "/v1/admin/devices/" + mine.id + "/update", 409, "device_offline"},
		{"admin route, not admin", user, "/v1/admin/devices/" + mine.id + "/update", 403, "forbidden"},
	}
	for _, c := range cases {
		var out apiErr
		if st := e.call(c.client, "POST", c.path, map[string]any{}, &out); st != c.status || out.Error.Code != c.code {
			t.Errorf("%s: %d %q, want %d %q", c.name, st, out.Error.Code, c.status, c.code)
		}
	}

	agent := e.online(mine, "darwin", "0.1.0")
	var acc struct{ Version string }
	if st := e.call(admin, "POST", "/v1/admin/devices/"+mine.id+"/update", nil, &acc); st != 202 || acc.Version != "0.2.0" {
		t.Fatalf("admin update now: %d %+v", st, acc)
	}
	var check proto.UpdateCheck
	agent.expect(proto.TypeUpdateCheck, &check)
	if !check.Explicit || check.Force {
		t.Fatalf("update.check %+v", check)
	}
	var actor string
	_ = e.pool.QueryRow(context.Background(), `SELECT actor FROM audit_events WHERE event = 'agent.update.requested'`).Scan(&actor)
	if actor != "admin" {
		t.Fatalf("requested by %q", actor)
	}
}

func TestPushUpdates(t *testing.T) {
	e := newEnv(t)
	e.app.hub.checkSpread = time.Millisecond
	admin := e.login(adminEmail, adminPass)
	e.release(admin, "0.2.0", "darwin", "universal", 100, true)
	e.release(admin, "0.2.0", "windows", "arm64", 100, true)

	outdated, current, failed, offline, win := e.enroll(admin), e.enroll(admin), e.enroll(admin), e.enroll(admin), e.enroll(admin)
	outdatedWS := e.online(outdated, "darwin", "0.1.0")
	currentWS := e.online(current, "darwin", "0.2.0")
	failedWS := e.online(failed, "darwin", "0.1.0")
	winWS := e.online(win, "windows", "0.1.0")
	_ = offline
	if _, err := e.pool.Exec(context.Background(), `UPDATE devices SET update_state = 'failed', update_version = '0.2.0' WHERE id = $1`, failed.id); err != nil {
		t.Fatal(err)
	}

	var out struct{ Queued int }
	if st := e.call(admin, "POST", "/v1/admin/updates/push", map[string]any{"os": "darwin"}, &out); st != 200 || out.Queued != 1 {
		t.Fatalf("push darwin: %d %+v", st, out)
	}
	var check proto.UpdateCheck
	outdatedWS.expect(proto.TypeUpdateCheck, &check)
	if !check.Explicit || check.Force {
		t.Fatalf("pushed update.check %+v", check)
	}
	if st := e.call(admin, "POST", "/v1/admin/updates/push", nil, &out); st != 200 || out.Queued != 2 {
		t.Fatalf("push all: %d %+v", st, out)
	}
	winWS.expect(proto.TypeUpdateCheck, nil)
	outdatedWS.expect(proto.TypeUpdateCheck, nil)
	var bad apiErr
	if st := e.call(admin, "POST", "/v1/admin/updates/push", map[string]any{"os": "linux"}, &bad); st != 400 {
		t.Fatalf("push linux: %d", st)
	}

	// Publishing a newer release asks every online darwin device it applies
	// to, including the one whose 0.2.0 attempt failed.
	rel := e.release(admin, "0.3.0", "darwin", "universal", 100, false)
	e.call(admin, "PATCH", "/v1/admin/releases/"+rel.ID, map[string]any{"published": true}, nil)
	for _, w := range []*wsClient{outdatedWS, currentWS, failedWS} {
		var c proto.UpdateCheck
		w.expect(proto.TypeUpdateCheck, &c)
		if c.Explicit || c.Force {
			t.Fatalf("publish update.check %+v", c)
		}
	}
}
