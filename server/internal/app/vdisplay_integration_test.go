package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func TestVirtualDisplayRequest(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	user := e.createUser(admin, "user@example.com")
	win := e.enroll(user)
	linux := e.enroll(user)
	offline := e.enroll(user)
	agent := e.online(win, "windows", "0.6.0")
	e.online(linux, "linux", "0.6.0")
	if _, err := e.pool.Exec(context.Background(), `UPDATE devices SET os = 'windows' WHERE id = $1`, offline.id); err != nil {
		t.Fatal(err)
	}
	install := map[string]any{"action": proto.VirtualDisplayInstall}

	var acc map[string]any
	if st := e.call(user, "POST", "/v1/devices/"+win.id+"/virtual-display", install, &acc); st != 202 || len(acc) != 0 {
		t.Fatalf("install: %d %+v", st, acc)
	}
	var req proto.VirtualDisplayRequest
	agent.expect(proto.TypeVirtualDisplay, &req)
	if req != (proto.VirtualDisplayRequest{V: proto.Version, Type: proto.TypeVirtualDisplay, Action: proto.VirtualDisplayInstall}) {
		t.Fatalf("virtual_display %+v", req)
	}
	var actor string
	var meta map[string]any
	_ = e.pool.QueryRow(context.Background(), `SELECT actor, meta FROM audit_events WHERE event = 'device.virtual_display.requested'`).Scan(&actor, &meta)
	if actor != "user" || meta["action"] != proto.VirtualDisplayInstall {
		t.Fatalf("device.virtual_display.requested %s %v", actor, meta)
	}

	cases := []struct {
		name   string
		client *http.Client
		path   string
		body   map[string]any
		status int
		code   string
	}{
		{"unknown action", user, "/v1/devices/" + win.id + "/virtual-display", map[string]any{"action": "toggle"}, 400, "bad_request"},
		{"no action", user, "/v1/devices/" + win.id + "/virtual-display", map[string]any{}, 400, "bad_request"},
		{"offline", user, "/v1/devices/" + offline.id + "/virtual-display", install, 409, "device_offline"},
		{"linux device", user, "/v1/devices/" + linux.id + "/virtual-display", install, 409, "unsupported"},
		{"someone else's device", admin, "/v1/devices/" + win.id + "/virtual-display", install, 404, "not_found"},
		{"admin route, not admin", user, "/v1/admin/devices/" + win.id + "/virtual-display", install, 403, "forbidden"},
	}
	for _, c := range cases {
		var out apiErr
		if st := e.call(c.client, "POST", c.path, c.body, &out); st != c.status || out.Error.Code != c.code {
			t.Errorf("%s: %d %q, want %d %q", c.name, st, out.Error.Code, c.status, c.code)
		}
	}

	if st := e.call(admin, "POST", "/v1/admin/devices/"+win.id+"/virtual-display", map[string]any{"action": proto.VirtualDisplayRemove}, nil); st != 202 {
		t.Fatalf("admin remove: %d", st)
	}
	agent.expect(proto.TypeVirtualDisplay, &req)
	if req.Action != proto.VirtualDisplayRemove {
		t.Fatalf("admin virtual_display %+v", req)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT actor, meta FROM audit_events WHERE event = 'device.virtual_display.requested'
		ORDER BY id DESC LIMIT 1`).Scan(&actor, &meta)
	if actor != "admin" || meta["action"] != proto.VirtualDisplayRemove {
		t.Fatalf("admin device.virtual_display.requested %s %v", actor, meta)
	}
	if n := e.eventCount("device.virtual_display.requested"); n != 2 {
		t.Fatalf("device.virtual_display.requested audited %d times", n)
	}
}
