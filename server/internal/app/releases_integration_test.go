package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

// The integration tests trust a generated release key instead of the
// committed one, whose private half is offline.
var testReleasePub, testReleasePriv = func() (string, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}()

func signSidecar(priv ed25519.PrivateKey, version, os, arch, filename string, data []byte) []byte {
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	b, _ := json.Marshal(proto.ReleaseSidecar{Format: proto.SidecarFormat, Version: version, OS: os, Arch: arch,
		SHA256: hexSum, Size: int64(len(data)), Filename: filename, KeyID: proto.KeyID(testReleasePub),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, proto.ReleaseMessage(version, os, arch, hexSum)))})
	return b
}

type formPart struct {
	name, filename string
	body           []byte
}

type releaseResp struct {
	Release store.Release `json:"release"`
	Error   struct{ Code string }
}

func (e *env) uploadRelease(c *http.Client, parts ...formPart) (int, releaseResp) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.filename != "" {
			w, err = mw.CreateFormFile(p.name, p.filename)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			e.t.Fatal(err)
		}
		_, _ = w.Write(p.body)
	}
	_ = mw.Close()
	resp, body := e.raw(c, "POST", "/v1/admin/releases", buf.Bytes(), "Content-Type", mw.FormDataContentType())
	var out releaseResp
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

// release uploads a signed installer and optionally publishes it.
func (e *env) release(admin *http.Client, version, os, arch string, rollout int, publish bool) store.Release {
	e.t.Helper()
	ext := proto.ExtForOS(os)
	data := []byte("installer " + version + " " + os + " " + arch)
	name := "RemoteAgent-" + version + ext
	st, out := e.uploadRelease(admin, formPart{name: "rollout_pct", body: []byte(strconv.Itoa(rollout))},
		formPart{name: "sig", filename: name + ".sig", body: signSidecar(testReleasePriv, version, os, arch, name, data)},
		formPart{name: "file", filename: name, body: data})
	if st != 200 {
		e.t.Fatalf("upload %s %s/%s: %d %+v", version, os, arch, st, out)
	}
	if publish {
		if st := e.call(admin, "PATCH", "/v1/admin/releases/"+out.Release.ID, map[string]any{"published": true}, &out); st != 200 {
			e.t.Fatalf("publish: %d", st)
		}
	}
	return out.Release
}

func (e *env) eventCount(event string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE event = $1`, event).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestReleaseUpload(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	user := e.createUser(admin, "user@example.com")
	anon := e.client()

	pkg := []byte("xar! pretend universal macOS installer")
	const version, name = "0.3.0", "RemoteAgent-0.3.0.pkg"
	sig := signSidecar(testReleasePriv, version, "darwin", "universal", name, pkg)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	tampered := bytes.Clone(pkg)
	tampered[0] ^= 1
	sigPart := formPart{name: "sig", filename: name + ".sig", body: sig}
	filePart := formPart{name: "file", filename: name, body: pkg}

	refused := []struct {
		name   string
		client *http.Client
		parts  []formPart
		status int
		code   string
	}{
		{"not admin", user, []formPart{sigPart, filePart}, 403, "forbidden"},
		{"signed by another key", admin, []formPart{{name: "sig", body: signSidecar(otherPriv, version, "darwin", "universal", name, pkg)}, filePart}, 400, "bad_signature"},
		{"sidecar is not JSON", admin, []formPart{{name: "sig", body: []byte("nope")}, filePart}, 400, "bad_signature"},
		{"sha mismatch", admin, []formPart{sigPart, {name: "file", filename: name, body: tampered}}, 400, "mismatch"},
		{"size mismatch", admin, []formPart{sigPart, {name: "file", filename: name, body: append(bytes.Clone(pkg), '!')}}, 400, "mismatch"},
		{"wrong extension", admin, []formPart{sigPart, {name: "file", filename: "RemoteAgent-0.3.0.msi", body: pkg}}, 400, "mismatch"},
		{"sig after file", admin, []formPart{filePart, sigPart}, 400, "bad_request"},
		{"no file", admin, []formPart{sigPart}, 400, "bad_request"},
		{"bad rollout", admin, []formPart{{name: "rollout_pct", body: []byte("101")}, sigPart, filePart}, 400, "bad_request"},
	}
	for _, c := range refused {
		if st, out := e.uploadRelease(c.client, c.parts...); st != c.status || out.Error.Code != c.code {
			t.Errorf("%s: %d %q, want %d %q", c.name, st, out.Error.Code, c.status, c.code)
		}
	}

	// rollout_pct defaults to 100.
	if st, out := e.uploadRelease(admin, sigPart, filePart); st != 200 || out.Release.RolloutPct != 100 {
		t.Fatalf("upload without rollout_pct: %d %+v", st, out)
	} else if st := e.call(admin, "DELETE", "/v1/admin/releases/"+out.Release.ID, nil, nil); st != 200 {
		t.Fatalf("delete: %d", st)
	}

	st, out := e.uploadRelease(admin, formPart{name: "rollout_pct", body: []byte("25")}, sigPart, filePart)
	if st != 200 {
		t.Fatalf("upload: %d %+v", st, out)
	}
	got := out.Release
	sum := sha256.Sum256(pkg)
	var sc proto.ReleaseSidecar
	_ = json.Unmarshal(sig, &sc)
	want := store.Release{ID: got.ID, Version: version, OS: "darwin", Arch: "universal", URL: "/v1/releases/" + got.ID + "/file",
		Filename: name, Size: int64(len(pkg)), SHA256: hex.EncodeToString(sum[:]), Signature: sc.Signature,
		KeyID: proto.KeyID(testReleasePub), RolloutPct: 25, CreatedAt: got.CreatedAt}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("release\n got %+v\nwant %+v", got, want)
	}
	if st, out := e.uploadRelease(admin, sigPart, filePart); st != 409 || out.Error.Code != "conflict" {
		t.Fatalf("duplicate: %d %+v", st, out)
	}
	if n := e.eventCount("release.uploaded"); n != 2 {
		t.Fatalf("release.uploaded audited %d times, want 2", n)
	}

	// Unpublished files are for admins only.
	if resp, _ := e.raw(anon, "GET", want.URL, nil); resp.StatusCode != 404 {
		t.Fatalf("anonymous unpublished: %d", resp.StatusCode)
	}
	if resp, body := e.raw(admin, "GET", want.URL, nil); resp.StatusCode != 200 || !bytes.Equal(body, pkg) {
		t.Fatalf("admin unpublished: %d %q", resp.StatusCode, body)
	}
	e.call(admin, "PATCH", "/v1/admin/releases/"+got.ID, map[string]any{"published": true}, nil)
	resp, body := e.raw(anon, "GET", want.URL, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, pkg) || resp.Header.Get("X-Content-SHA256") != want.SHA256 ||
		resp.Header.Get("ETag") != `"`+want.SHA256+`"` || resp.Header.Get("Content-Disposition") != `attachment; filename=`+name {
		t.Fatalf("published download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, body = e.raw(anon, "GET", want.URL, nil, "Range", "bytes=0-3"); resp.StatusCode != 206 || string(body) != "xar!" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}

	// The release's file becomes the Add-device installer.
	if st := e.call(admin, "POST", "/v1/admin/releases/"+got.ID+"/installer", nil, &out); st != 200 || !out.Release.Installer {
		t.Fatalf("installer: %d %+v", st, out)
	}
	if n := e.eventCount("release.installer_set"); n != 1 {
		t.Fatalf("release.installer_set audited %d times", n)
	}
	if resp, body = e.raw(anon, "GET", "/v1/downloads/macos", nil); resp.StatusCode != 200 || !bytes.Equal(body, pkg) ||
		resp.Header.Get("Content-Disposition") != `attachment; filename=`+name {
		t.Fatalf("download after installer: %d %q", resp.StatusCode, body)
	}
	var list struct{ Items []store.Release }
	e.call(admin, "GET", "/v1/admin/releases", nil, &list)
	if len(list.Items) != 1 || !list.Items[0].Installer || list.Items[0].PublishedAt == nil {
		t.Fatalf("list: %+v", list)
	}

	// Deleting the release keeps the installer it provided.
	if st := e.call(admin, "DELETE", "/v1/admin/releases/"+got.ID, nil, nil); st != 200 {
		t.Fatalf("delete: %d", st)
	}
	if resp, _ = e.raw(anon, "GET", want.URL, nil); resp.StatusCode != 404 {
		t.Fatalf("file after delete: %d", resp.StatusCode)
	}
	if resp, body = e.raw(anon, "GET", "/v1/downloads/macos", nil); resp.StatusCode != 200 || !bytes.Equal(body, pkg) {
		t.Fatalf("download after release delete: %d", resp.StatusCode)
	}
	var cfg struct {
		Downloads map[string]*struct {
			File *store.AgentDownload `json:"file"`
		} `json:"downloads"`
	}
	e.call(anon, "GET", "/v1/config", nil, &cfg)
	if f := cfg.Downloads["macos"].File; f == nil || f.ReleaseID != nil || f.SHA256 != want.SHA256 {
		t.Fatalf("config download after release delete: %+v", f)
	}
}

func TestAgentUpdateSelection(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	ag := e.enroll(admin)
	bucket := proto.RolloutBucket(ag.id)
	bearer := "Bearer " + ag.tok
	check := func(query string) (int, map[string]any) {
		t.Helper()
		var out map[string]any
		st := e.call(e.client(), "GET", "/v1/agent/update?os=darwin&arch=arm64&version=0.1.0"+query, nil, &out, "Authorization", bearer)
		return st, out
	}
	if st, _ := check(""); st != 204 {
		t.Fatalf("no releases: %d, want 204", st)
	}

	e.release(admin, "0.2.0", "darwin", "universal", 100, true)
	e.release(admin, "0.4.0", "darwin", "arm64", 100, false)
	newest := e.release(admin, "0.3.0", "darwin", "arm64", bucket, true)

	st, man := check("")
	if st != 200 || man["version"] != "0.2.0" || man["arch"] != "universal" {
		t.Fatalf("outside the 0.3.0 cohort: %d %v", st, man)
	}
	st, man = check("&explicit=1")
	wantMan := map[string]any{"version": "0.3.0", "os": "darwin", "arch": "arm64", "url": newest.URL,
		"sha256": newest.SHA256, "size": float64(newest.Size), "signature": newest.Signature}
	if st != 200 || !reflect.DeepEqual(man, wantMan) {
		t.Fatalf("explicit: %d\n got %v\nwant %v", st, man, wantMan)
	}
	if err := proto.VerifyRelease(testReleasePub, "0.3.0", "darwin", "arm64", newest.SHA256, newest.Signature); err != nil {
		t.Fatalf("manifest signature: %v", err)
	}
	e.call(admin, "PATCH", "/v1/admin/releases/"+newest.ID, map[string]any{"rollout_pct": bucket + 1}, nil)
	if st, man = check(""); st != 200 || man["version"] != "0.3.0" {
		t.Fatalf("inside the cohort: %d %v", st, man)
	}

	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{AgentVersion: "0.1.0", OS: "darwin", Arch: "arm64", ActiveSessions: []string{}})
	var ack proto.HelloAck
	agent.expect(proto.TypeHelloAck, &ack)
	if ack.Update != (proto.UpdateHint{Available: true, Version: "0.3.0"}) {
		t.Fatalf("hello_ack update %+v", ack.Update)
	}

	var dev struct{ Device store.Device }
	e.call(admin, "GET", "/v1/devices/"+ag.id, nil, &dev)
	if v := dev.Device.LatestVersion; v == nil || *v != "0.3.0" || !dev.Device.UpdateAvailable {
		t.Fatalf("device latest %v available %v", v, dev.Device.UpdateAvailable)
	}
	var list struct{ Items []store.Device }
	e.call(admin, "GET", "/v1/admin/devices", nil, &list)
	if len(list.Items) != 1 || list.Items[0].LatestVersion == nil || *list.Items[0].LatestVersion != "0.3.0" {
		t.Fatalf("admin devices %+v", list.Items)
	}

	agent.send(proto.TypeHello, proto.Hello{AgentVersion: "0.3.0", OS: "darwin", Arch: "arm64", ActiveSessions: []string{}})
	var ack2 proto.HelloAck
	agent.expect(proto.TypeHelloAck, &ack2)
	if ack2.Update != (proto.UpdateHint{}) {
		t.Fatalf("hello_ack on the latest version %+v", ack2.Update)
	}
	var rels struct{ Items []store.Release }
	e.call(admin, "GET", "/v1/admin/releases", nil, &rels)
	versions, counts := []string{}, []int{}
	for _, r := range rels.Items {
		versions = append(versions, r.Version)
		counts = append(counts, r.Devices)
	}
	if strings.Join(versions, ",") != "0.4.0,0.3.0,0.2.0" || !reflect.DeepEqual(counts, []int{0, 1, 0}) {
		t.Fatalf("releases %v devices %v", versions, counts)
	}
}
