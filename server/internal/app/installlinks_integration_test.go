package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

type installLink struct {
	Code       string     `json:"code"`
	URL        string     `json:"url"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	UseCount   int        `json:"use_count"`
}

type installLinkResp struct {
	Link *installLink `json:"link"`
}

type installFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// enrollWith enrolls a fresh device key with code (a token or an install link
// code) and returns the status and device id.
func (e *env) enrollWith(code, hostname string) (int, string) {
	e.t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	var en struct {
		DeviceID string `json:"device_id"`
	}
	st := e.call(e.client(), "POST", "/v1/agent/enroll", map[string]any{"token": code,
		"public_key": base64.StdEncoding.EncodeToString(pub), "hostname": hostname, "os": "windows", "arch": "amd64", "version": "0.6.4"}, &en)
	return st, en.DeviceID
}

func (e *env) enrollMeta(deviceID string) map[string]any {
	e.t.Helper()
	var raw []byte
	if err := e.pool.QueryRow(context.Background(),
		`SELECT meta FROM audit_events WHERE device_id = $1 AND event = 'device.enrolled'`, deviceID).Scan(&raw); err != nil {
		e.t.Fatal(err)
	}
	var meta map[string]any
	_ = json.Unmarshal(raw, &meta)
	return meta
}

func TestInstallLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	user := e.createUser(admin, "owner@example.com")
	anon := e.client()
	msi := []byte("MSI pretend Windows installer")
	pkg := []byte("xar! pretend macOS installer")
	e.raw(admin, "PUT", "/v1/admin/downloads/windows?filename=RemoteAgent-0.6.4-x64.msi", msi)
	e.raw(admin, "PUT", "/v1/admin/downloads/macos?filename=RemoteAgent-0.6.4.pkg", pkg)

	var got installLinkResp
	if st := e.call(user, "GET", "/v1/install-link", nil, &got); st != 200 || got.Link != nil {
		t.Fatalf("before creating: %d %+v", st, got.Link)
	}
	if st := e.call(anon, "GET", "/v1/install-link", nil, nil); st != 401 {
		t.Fatalf("anonymous: %d, want 401", st)
	}

	var created installLinkResp
	if st := e.call(user, "POST", "/v1/install-link", map[string]any{}, &created); st != 200 || created.Link == nil {
		t.Fatalf("create: %d %+v", st, created.Link)
	}
	code := created.Link.Code
	if len(code) != 14 || code[4] != '-' || code[9] != '-' || created.Link.URL != testOrigin+"/i/"+code ||
		created.Link.UseCount != 0 || created.Link.LastUsedAt != nil {
		t.Fatalf("created link %+v", created.Link)
	}
	if e.call(user, "GET", "/v1/install-link", nil, &got); got.Link == nil || got.Link.Code != code {
		t.Fatalf("read back %+v, want code %s", got.Link, code)
	}

	// The landing page lists the installers named for this link; nothing for Windows 7.
	var info struct {
		Downloads map[string]*installFile `json:"downloads"`
	}
	if st := e.call(anon, "GET", "/v1/install/"+strings.ToLower(code), nil, &info); st != 200 {
		t.Fatalf("install info: %d", st)
	}
	wantInfo := map[string]*installFile{
		"windows":  {Filename: proto.InstallLinkFilename("RemoteAgent-0.6.4-x64.msi", code), Size: int64(len(msi))},
		"macos":    {Filename: proto.InstallLinkFilename("RemoteAgent-0.6.4.pkg", code), Size: int64(len(pkg))},
		"windows7": nil,
	}
	for os, want := range wantInfo {
		if g := info.Downloads[os]; (g == nil) != (want == nil) || (g != nil && *g != *want) {
			t.Errorf("downloads[%s] = %+v, want %+v", os, g, want)
		}
	}

	resp, body := e.raw(anon, "GET", "/v1/install/"+code+"/windows", nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, msi) ||
		resp.Header.Get("Content-Disposition") != `attachment; filename=RemoteAgent-0.6.4-x64.link-`+code+`.msi` {
		t.Fatalf("link download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, _ = e.raw(anon, "GET", "/v1/install/"+code+"/windows7", nil); resp.StatusCode != 404 {
		t.Fatalf("missing installer: %d, want 404", resp.StatusCode)
	}

	// The code enrolls any number of devices into the owner's account, with the
	// same defaults as a token (remembered browsers connect without a prompt).
	var ids []string
	for _, host := range []string{"front-desk", "back-office"} {
		st, id := e.enrollWith(strings.ToLower(code), host)
		if st != 200 {
			t.Fatalf("enroll %s: %d", host, st)
		}
		ids = append(ids, id)
	}
	var devs struct {
		Items []store.Device `json:"items"`
	}
	e.call(user, "GET", "/v1/devices", nil, &devs)
	if len(devs.Items) != 2 || !devs.Items[0].AutoAccept || !devs.Items[1].AutoAccept {
		t.Fatalf("owner's devices %+v", devs.Items)
	}
	if meta := e.enrollMeta(ids[0]); meta["via"] != "install_link" || meta["hostname"] != "front-desk" {
		t.Fatalf("audit meta %v", meta)
	}
	if e.call(user, "GET", "/v1/install-link", nil, &got); got.Link.UseCount != 2 || got.Link.LastUsedAt == nil {
		t.Fatalf("after two enrollments %+v", got.Link)
	}

	// A new link retires the old code everywhere.
	var rotated installLinkResp
	e.call(user, "POST", "/v1/install-link", map[string]any{}, &rotated)
	if rotated.Link == nil || rotated.Link.Code == code || rotated.Link.UseCount != 0 {
		t.Fatalf("rotated %+v", rotated.Link)
	}
	if st := e.call(anon, "GET", "/v1/install/"+code, nil, nil); st != 404 {
		t.Fatalf("old code info: %d, want 404", st)
	}
	if resp, _ = e.raw(anon, "GET", "/v1/install/"+code+"/windows", nil); resp.StatusCode != 404 {
		t.Fatalf("old code download: %d, want 404", resp.StatusCode)
	}
	if st, _ := e.enrollWith(code, "late"); st != 401 {
		t.Fatalf("old code enroll: %d, want 401", st)
	}

	// A disabled owner's link stops enrolling.
	var me struct{ User struct{ ID string } }
	e.call(user, "GET", "/v1/auth/me", nil, &me)
	newCode := rotated.Link.Code
	e.call(admin, "PATCH", "/v1/admin/users/"+me.User.ID, map[string]any{"disabled": true}, nil)
	if st, _ := e.enrollWith(newCode, "disabled-owner"); st != 401 {
		t.Fatalf("disabled owner enroll: %d, want 401", st)
	}
	if st := e.call(anon, "GET", "/v1/install/"+newCode, nil, nil); st != 404 {
		t.Fatalf("disabled owner info: %d, want 404", st)
	}
	e.call(admin, "PATCH", "/v1/admin/users/"+me.User.ID, map[string]any{"disabled": false}, nil)
	user = e.login("owner@example.com", userPass)

	if st := e.call(user, "DELETE", "/v1/install-link", nil, nil); st != 200 {
		t.Fatalf("revoke: %d", st)
	}
	if e.call(user, "GET", "/v1/install-link", nil, &got); got.Link != nil {
		t.Fatalf("after revoke %+v", got.Link)
	}
	if st, _ := e.enrollWith(newCode, "after-revoke"); st != 401 {
		t.Fatalf("revoked code enroll: %d, want 401", st)
	}
	if st := e.call(user, "DELETE", "/v1/install-link", nil, nil); st != 404 {
		t.Fatalf("second revoke: %d, want 404", st)
	}
}

func TestInstallLinksAreSeparatePerUser(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	a := e.createUser(admin, "a@example.com")
	b := e.createUser(admin, "b@example.com")
	var la, lb installLinkResp
	e.call(a, "POST", "/v1/install-link", map[string]any{}, &la)
	e.call(b, "POST", "/v1/install-link", map[string]any{}, &lb)
	if la.Link == nil || lb.Link == nil || la.Link.Code == lb.Link.Code {
		t.Fatalf("links %+v %+v", la.Link, lb.Link)
	}
	if st, _ := e.enrollWith(la.Link.Code, "for-a"); st != 200 {
		t.Fatalf("enroll: %d", st)
	}
	var da, db struct {
		Items []store.Device `json:"items"`
	}
	e.call(a, "GET", "/v1/devices", nil, &da)
	e.call(b, "GET", "/v1/devices", nil, &db)
	if len(da.Items) != 1 || da.Items[0].Name != "for-a" || len(db.Items) != 0 {
		t.Fatalf("a has %+v, b has %+v", da.Items, db.Items)
	}
}
