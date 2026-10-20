package app

// DB integration tests. They need a Postgres server:
//   TEST_DATABASE_URL=postgres://postgres:test@host:5432/postgres?sslmode=disable
// Each test creates and drops its own database.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/config"
	"github.com/ruolez/remotectl/server/internal/db"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

const (
	testOrigin = "http://rc.test"
	adminEmail = "admin@example.com"
	adminPass  = "admin-password-123"
	userPass   = "user-password-1234"
)

type env struct {
	t    *testing.T
	app  *App
	pool *pgxpool.Pool
	srv  *httptest.Server
	cfg  *config.Config
}

func testConfig(t *testing.T, dbURL string) *config.Config {
	downloadsDir := t.TempDir()
	cfg, err := config.Load(func(k string) string {
		return map[string]string{
			"DATABASE_URL": dbURL, "PUBLIC_URL": testOrigin, "JWT_SECRET": "test-jwt-secret-0123456789",
			"TURN_SECRET": "turn-secret", "TOTP_ENC_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
			"TURN_URLS":       "stun:localhost:3478,turn:localhost:3478?transport=udp",
			"TURN_URLS_AGENT": "stun:coturn:3478,turn:coturn:3478?transport=udp",
			"ADMIN_EMAIL":     adminEmail, "ADMIN_INITIAL_PASSWORD": adminPass, "LOG_LEVEL": "error",
			"DOWNLOADS_DIR": downloadsDir, "RELEASE_PUBLIC_KEY": testReleasePub,
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// testDB creates a scratch database and returns its URL.
func testDB(t *testing.T) string {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("rc_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

var cheapArgon = security.ArgonParams{Memory: 1024, Time: 1, Threads: 1}

func newEnv(t *testing.T) *env {
	dbURL := testDB(t)
	cfg := testConfig(t, dbURL)
	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, err := New(ctx, cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Argon: &cheapArgon})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Router())
	t.Cleanup(srv.Close)
	return &env{t: t, app: a, pool: pool, srv: srv, cfg: cfg}
}

func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

// call sends JSON and decodes the response into out (if non-nil); returns the status.
func (e *env) call(c *http.Client, method, path string, body, out any, hdr ...string) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

func (e *env) login(email, pw string) *http.Client {
	e.t.Helper()
	c := e.client()
	if st := e.call(c, "POST", "/v1/auth/login", map[string]string{"email": email, "password": pw}, nil); st != 200 {
		e.t.Fatalf("login %s: status %d", email, st)
	}
	return c
}

func (e *env) createUser(admin *http.Client, email string) *http.Client {
	e.t.Helper()
	if st := e.call(admin, "POST", "/v1/admin/users", map[string]any{"email": email, "password": userPass}, nil); st != 200 {
		e.t.Fatalf("create user: %d", st)
	}
	return e.login(email, userPass)
}

type fakeAgent struct {
	id   string
	priv ed25519.PrivateKey
	tok  string
}

func (e *env) enroll(c *http.Client) *fakeAgent {
	e.t.Helper()
	var et struct{ Token string }
	if st := e.call(c, "POST", "/v1/enroll-tokens", map[string]any{}, &et); st != 200 {
		e.t.Fatalf("enroll token: %d", st)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var en struct {
		DeviceID string `json:"device_id"`
	}
	if st := e.call(e.client(), "POST", "/v1/agent/enroll", map[string]any{"token": et.Token, "public_key": base64.StdEncoding.EncodeToString(pub),
		"hostname": "mac-mini", "os": "darwin", "arch": "arm64", "version": "0.1.0"}, &en); st != 200 {
		e.t.Fatalf("enroll: %d", st)
	}
	ag := &fakeAgent{id: en.DeviceID, priv: priv}
	ag.tok = e.agentAuth(ag)
	return ag
}

func (e *env) agentAuth(ag *fakeAgent) string {
	e.t.Helper()
	var ch struct{ Nonce string }
	if st := e.call(e.client(), "POST", "/v1/agent/challenge", map[string]string{"device_id": ag.id}, &ch); st != 200 {
		e.t.Fatalf("challenge: %d", st)
	}
	sig := ed25519.Sign(ag.priv, proto.AuthChallengeMessage(ag.id, ch.Nonce))
	var au struct{ Token string }
	if st := e.call(e.client(), "POST", "/v1/agent/auth", map[string]string{"device_id": ag.id, "nonce": ch.Nonce,
		"signature": base64.StdEncoding.EncodeToString(sig)}, &au); st != 200 {
		e.t.Fatalf("auth: %d", st)
	}
	return au.Token
}

func wsURL(httpURL, path string) string { return "ws" + strings.TrimPrefix(httpURL, "http") + path }

type wsClient struct {
	t *testing.T
	c *websocket.Conn
}

func (e *env) dialBrowser(c *http.Client) *wsClient {
	e.t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL(e.srv.URL, "/v1/ws"),
		&websocket.DialOptions{HTTPClient: c, HTTPHeader: http.Header{"Origin": {testOrigin}}})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { conn.CloseNow() })
	return &wsClient{t: e.t, c: conn}
}

func (e *env) dialAgent(ag *fakeAgent) *wsClient {
	e.t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL(e.srv.URL, "/v1/agent/ws"),
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + ag.tok}}})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { conn.CloseNow() })
	return &wsClient{t: e.t, c: conn}
}

func (w *wsClient) send(typ string, msg any) {
	w.t.Helper()
	b, err := proto.Encode(typ, msg)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		w.t.Fatal(err)
	}
}

// expect reads until a message of type typ arrives (skipping presence pushes).
func (w *wsClient) expect(typ string, out any) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := w.c.Read(ctx)
		if err != nil {
			w.t.Fatalf("waiting for %s: %v", typ, err)
		}
		var env proto.Envelope
		_ = json.Unmarshal(data, &env)
		if env.Type == typ {
			if out != nil {
				if err := json.Unmarshal(data, out); err != nil {
					w.t.Fatal(err)
				}
			}
			return
		}
		if env.Type == proto.TypeError && typ != proto.TypeError {
			w.t.Fatalf("waiting for %s, got error: %s", typ, data)
		}
	}
}

func (e *env) registerClientKey(c *http.Client) string {
	e.t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	var out struct {
		ClientKey struct{ ID string } `json:"client_key"`
	}
	if st := e.call(c, "POST", "/v1/client-keys", map[string]string{"public_key": base64.StdEncoding.EncodeToString(der), "label": "Chrome on Mac"}, &out); st != 200 {
		e.t.Fatalf("client key: %d", st)
	}
	return out.ClientKey.ID
}

func (e *env) auditEvents(sessionID string) []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT event FROM audit_events WHERE session_id = $1 ORDER BY id`, sessionID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------

func TestPasswordAndTOTPLogin(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)

	var me struct {
		User struct {
			Email       string `json:"email"`
			IsAdmin     bool   `json:"is_admin"`
			TOTPEnabled bool   `json:"totp_enabled"`
		} `json:"user"`
	}
	if st := e.call(c, "GET", "/v1/auth/me", nil, &me); st != 200 || me.User.Email != adminEmail || !me.User.IsAdmin {
		t.Fatalf("me: %d %+v", st, me)
	}

	var setup struct {
		Secret string `json:"secret"`
		QR     string `json:"qr_png"`
	}
	if st := e.call(c, "POST", "/v1/account/totp/setup", map[string]any{}, &setup); st != 200 || !strings.HasPrefix(setup.QR, "data:image/png;base64,") {
		t.Fatalf("setup: %d", st)
	}
	code, _ := totp.GenerateCode(setup.Secret, time.Now())
	if st := e.call(c, "POST", "/v1/account/totp/enable", map[string]string{"code": code}, nil); st != 200 {
		t.Fatalf("enable: %d", st)
	}
	e.call(c, "POST", "/v1/auth/logout", map[string]any{}, nil)
	if st := e.call(c, "GET", "/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("after logout me = %d, want 401", st)
	}

	c2 := e.client()
	var step1 struct {
		TOTPRequired bool   `json:"totp_required"`
		Ticket       string `json:"ticket"`
	}
	e.call(c2, "POST", "/v1/auth/login", map[string]string{"email": adminEmail, "password": adminPass}, &step1)
	if !step1.TOTPRequired || step1.Ticket == "" {
		t.Fatalf("expected totp_required, got %+v", step1)
	}
	if st := e.call(c2, "POST", "/v1/auth/login/totp", map[string]string{"ticket": step1.Ticket, "code": "000000"}, nil); st != 401 {
		t.Fatalf("wrong code accepted: %d", st)
	}
	code, _ = totp.GenerateCode(setup.Secret, time.Now())
	if st := e.call(c2, "POST", "/v1/auth/login/totp", map[string]string{"ticket": step1.Ticket, "code": code}, &me); st != 200 || !me.User.TOTPEnabled {
		t.Fatalf("totp login: %d %+v", st, me)
	}

	// A code is single-use: replaying it within its window is refused.
	c3 := e.client()
	e.call(c3, "POST", "/v1/auth/login", map[string]string{"email": adminEmail, "password": adminPass}, &step1)
	if st := e.call(c3, "POST", "/v1/auth/login/totp", map[string]string{"ticket": step1.Ticket, "code": code}, nil); st != 401 {
		t.Fatalf("replayed totp code: got %d, want 401", st)
	}

	// 10 failures per email in 15 minutes → 429, even with the right password.
	for i := 0; i < 10; i++ {
		e.call(e.client(), "POST", "/v1/auth/login", map[string]string{"email": "nobody@example.com", "password": "wrong-password"}, nil)
	}
	if st := e.call(e.client(), "POST", "/v1/auth/login", map[string]string{"email": "nobody@example.com", "password": "x"}, nil); st != 429 {
		t.Fatalf("rate limit: got %d, want 429", st)
	}

	// Cross-origin state change is refused.
	if st := e.call(c2, "POST", "/v1/enroll-tokens", map[string]any{}, nil, "Origin", "https://evil.example"); st != 403 {
		t.Fatalf("cross-origin POST: got %d, want 403", st)
	}
}

// The local dev setup signs in as a bare "admin"; login must not demand an
// email-shaped identifier.
func TestLoginAcceptsPlainUsername(t *testing.T) {
	e := newEnv(t)
	const username = "admin"
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET email = $1 WHERE email = $2`, username, adminEmail); err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{username, " Admin "} {
		var me struct {
			User struct {
				Email string `json:"email"`
			} `json:"user"`
		}
		if st := e.call(e.login(typed, adminPass), "GET", "/v1/auth/me", nil, &me); st != 200 || me.User.Email != username {
			t.Fatalf("login as %q: me = %d %+v", typed, st, me)
		}
	}
}

// raw sends body as-is and returns the response with its body read.
func (e *env) raw(c *http.Client, method, path string, body []byte, hdr ...string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestAgentDownloads(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	user := e.createUser(admin, "user@example.com")
	anon := e.client()
	pkg := []byte("xar! pretend macOS installer")
	sum := sha256.Sum256(pkg)

	type link struct {
		URL  string               `json:"url"`
		File *store.AgentDownload `json:"file"`
	}
	downloads := func() map[string]*link {
		var cfg struct {
			Downloads map[string]*link `json:"downloads"`
		}
		e.call(anon, "GET", "/v1/config", nil, &cfg)
		return cfg.Downloads
	}
	if d := downloads(); d["macos"] != nil || d["windows"] != nil {
		t.Fatalf("fresh install advertises downloads: %+v", d)
	}

	if resp, _ := e.raw(user, "PUT", "/v1/admin/downloads/macos?filename=RemoteAgent.pkg", pkg); resp.StatusCode != 403 {
		t.Fatalf("non-admin upload: %d, want 403", resp.StatusCode)
	}
	if resp, _ := e.raw(admin, "PUT", "/v1/admin/downloads/macos?filename=RemoteAgent.msi", pkg); resp.StatusCode != 400 {
		t.Fatalf("wrong extension: %d, want 400", resp.StatusCode)
	}
	if resp, _ := e.raw(admin, "PUT", "/v1/admin/downloads/macos?filename=RemoteAgent.pkg", nil); resp.StatusCode != 400 {
		t.Fatalf("empty upload: %d, want 400", resp.StatusCode)
	}
	resp, body := e.raw(admin, "PUT", "/v1/admin/downloads/macos?filename=RemoteAgent-1.0.pkg", pkg, "Origin", testOrigin)
	var up store.AgentDownload
	json.Unmarshal(body, &up)
	if resp.StatusCode != 200 || up.SHA256 != hex.EncodeToString(sum[:]) || up.Size != int64(len(pkg)) || up.Filename != "RemoteAgent-1.0.pkg" {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}

	d := downloads()
	if d["macos"] == nil || d["macos"].URL != "/v1/downloads/macos" || d["macos"].File == nil || d["macos"].File.SHA256 != up.SHA256 || d["windows"] != nil {
		t.Fatalf("config after upload: %+v", d)
	}

	resp, body = e.raw(anon, "GET", "/v1/downloads/macos", nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, pkg) ||
		resp.Header.Get("Content-Disposition") != `attachment; filename=RemoteAgent-1.0.pkg` ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("anonymous download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, body = e.raw(anon, "GET", "/v1/downloads/macos", nil, "Range", "bytes=0-3"); resp.StatusCode != 206 || string(body) != "xar!" {
		t.Fatalf("range request: %d %q", resp.StatusCode, body)
	}
	if resp, _ = e.raw(anon, "GET", "/v1/downloads/macos", nil, "If-None-Match", `"`+up.SHA256+`"`); resp.StatusCode != 304 {
		t.Fatalf("conditional request: %d, want 304", resp.StatusCode)
	}

	newer := []byte("xar! newer installer")
	e.raw(admin, "PUT", "/v1/admin/downloads/macos?filename=RemoteAgent-1.1.pkg", newer)
	if _, body = e.raw(anon, "GET", "/v1/downloads/macos", nil); !bytes.Equal(body, newer) {
		t.Fatalf("replacement not served: %q", body)
	}

	if resp, _ = e.raw(admin, "DELETE", "/v1/admin/downloads/macos", nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ = e.raw(anon, "GET", "/v1/downloads/macos", nil); resp.StatusCode != 404 {
		t.Fatalf("after delete: %d, want 404", resp.StatusCode)
	}
	if d := downloads(); d["macos"] != nil {
		t.Fatalf("config after delete: %+v", d["macos"])
	}
	if resp, _ = e.raw(admin, "DELETE", "/v1/admin/downloads/macos", nil); resp.StatusCode != 404 {
		t.Fatalf("second delete: %d, want 404", resp.StatusCode)
	}
}

func TestEnrollmentAndAgentAuth(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	var et struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	e.call(c, "POST", "/v1/enroll-tokens", map[string]any{}, &et)
	if len(et.Token) != 9 || et.Token[4] != '-' || time.Until(et.ExpiresAt) > 15*time.Minute || time.Until(et.ExpiresAt) < 14*time.Minute {
		t.Fatalf("token %+v", et)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body := map[string]any{"token": strings.ToLower(et.Token), "public_key": base64.StdEncoding.EncodeToString(pub),
		"hostname": "studio", "os": "darwin", "arch": "arm64", "version": "0.1.0"}
	var en struct {
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
	}
	if st := e.call(e.client(), "POST", "/v1/agent/enroll", body, &en); st != 200 || en.Name != "studio" {
		t.Fatalf("enroll: %d %+v", st, en)
	}
	if st := e.call(e.client(), "POST", "/v1/agent/enroll", body, nil); st != 401 {
		t.Fatalf("token reuse: got %d, want 401", st)
	}

	var ch struct{ Nonce string }
	e.call(e.client(), "POST", "/v1/agent/challenge", map[string]string{"device_id": en.DeviceID}, &ch)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	bad := ed25519.Sign(otherPriv, proto.AuthChallengeMessage(en.DeviceID, ch.Nonce))
	if st := e.call(e.client(), "POST", "/v1/agent/auth", map[string]string{"device_id": en.DeviceID, "nonce": ch.Nonce,
		"signature": base64.StdEncoding.EncodeToString(bad)}, nil); st != 401 {
		t.Fatalf("wrong key accepted: %d", st)
	}
	ag := &fakeAgent{id: en.DeviceID, priv: priv}
	ag.tok = e.agentAuth(ag)
	if id, err := security.ParseAgentJWT(e.cfg.JWTSecret, ag.tok); err != nil || id != en.DeviceID {
		t.Fatalf("jwt: %q %v", id, err)
	}

	// Uninstall marks the device removed; auth is refused afterwards.
	if st := e.call(e.client(), "DELETE", "/v1/agent/self", nil, nil, "Authorization", "Bearer "+ag.tok); st != 200 {
		t.Fatalf("self delete: %d", st)
	}
	var errBody struct {
		Error struct{ Code string } `json:"error"`
	}
	if st := e.call(e.client(), "POST", "/v1/agent/challenge", map[string]string{"device_id": en.DeviceID}, &errBody); st != 403 || errBody.Error.Code != "device_removed" {
		t.Fatalf("challenge after removal: %d %+v", st, errBody)
	}
}

func TestSignalingRelayEndToEnd(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	keyID := e.registerClientKey(c)

	browser := e.dialBrowser(c)
	agent := e.dialAgent(ag)
	var st proto.DeviceStatus
	browser.expect(proto.TypeDeviceStatus, &st)
	if st.DeviceID != ag.id || !st.Online {
		t.Fatalf("device.status %+v", st)
	}
	agent.send(proto.TypeHello, proto.Hello{AgentVersion: "0.1.0", OS: "darwin", Arch: "arm64", Hostname: "mac-mini",
		Capabilities: proto.Capabilities{Monitors: []proto.Monitor{{ID: 1, Name: "Built-in", Width: 3024, Height: 1964, Scale: 2, Primary: true}},
			Codecs: []string{"h264"}, MaxFPS: 60, Encoder: "videotoolbox"},
		Permissions: proto.Permissions{Screen: true, Input: true}, ActiveSessions: []string{}})
	var ack proto.HelloAck
	agent.expect(proto.TypeHelloAck, &ack)
	if ack.Device.ID != ag.id || ack.Device.Name != "mac-mini" {
		t.Fatalf("hello_ack %+v", ack)
	}

	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r1", DeviceID: ag.id, ClientKeyID: keyID, ForceRelay: true})
	var created proto.SessionCreated
	browser.expect(proto.TypeSessionCreated, &created)
	if created.Req != "r1" || created.SessionID == "" || !created.ForceRelay || len(created.ICEServers) != 2 ||
		created.ICEServers[0].Credential != "" || created.ICEServers[1].Username == "" {
		t.Fatalf("session.created %+v", created)
	}

	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r2", DeviceID: ag.id, ClientKeyID: keyID})
	var busy proto.Error
	browser.expect(proto.TypeError, &busy)
	if busy.Code != proto.ErrDeviceBusy || busy.Req != "r2" {
		t.Fatalf("second create: %+v", busy)
	}

	sid := created.SessionID
	browser.send(proto.TypeSessionOffer, proto.SessionOffer{SessionID: sid, SDP: "v=0 offer", FPSig: "sig-b"})
	var offer proto.SessionOffer
	agent.expect(proto.TypeSessionOffer, &offer)
	if offer.SDP != "v=0 offer" || offer.ClientKey == nil || offer.ClientKey.ID != keyID || offer.User.Email != adminEmail ||
		len(offer.ICEServers) != 2 || offer.ICEServers[1].URLs[0] != "turn:coturn:3478?transport=udp" ||
		!strings.HasSuffix(offer.ICEServers[1].Username, ":"+sid+":a") {
		t.Fatalf("agent offer %+v", offer)
	}

	agent.send(proto.TypeSessionStatus, proto.SessionStatus{SessionID: sid, Status: proto.StatusAwaitingApproval})
	var status proto.SessionStatus
	browser.expect(proto.TypeSessionStatus, &status)
	if status.Status != proto.StatusAwaitingApproval {
		t.Fatalf("status %+v", status)
	}
	agent.send(proto.TypeSessionAnswer, proto.SessionAnswer{SessionID: sid, SDP: "v=0 answer", FPSig: "sig-a"})
	var ans proto.SessionAnswer
	browser.expect(proto.TypeSessionAnswer, &ans)
	if ans.SDP != "v=0 answer" || ans.FPSig != "sig-a" {
		t.Fatalf("answer %+v", ans)
	}
	cand := &proto.ICECandidate{Candidate: "candidate:1 1 udp 1 10.0.0.1 5000 typ host"}
	browser.send(proto.TypeICECandidate, proto.ICECandidateMsg{SessionID: sid, Candidate: cand})
	var gotCand proto.ICECandidateMsg
	agent.expect(proto.TypeICECandidate, &gotCand)
	if gotCand.Candidate == nil || gotCand.Candidate.Candidate != cand.Candidate {
		t.Fatalf("browser→agent candidate %+v", gotCand)
	}
	agent.send(proto.TypeICECandidate, proto.ICECandidateMsg{SessionID: sid, Candidate: cand})
	browser.expect(proto.TypeICECandidate, &gotCand)

	agent.send(proto.TypeSessionState, proto.SessionState{SessionID: sid, Connected: true, Path: proto.PathRelay, LocalCandidate: "host", RemoteCandidate: "relay"})
	var state proto.SessionState
	browser.expect(proto.TypeSessionState, &state)
	if !state.Connected || state.Path != proto.PathRelay {
		t.Fatalf("state %+v", state)
	}
	var dbState, dbPath string
	_ = e.pool.QueryRow(context.Background(), `SELECT state, coalesce(ice_path, '') FROM sessions WHERE id = $1`, sid).Scan(&dbState, &dbPath)
	if dbState != "active" || dbPath != "relay" {
		t.Fatalf("db after connect: %s %s", dbState, dbPath)
	}

	agent.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sid, Reason: proto.EndAgentKill, BytesSent: 1000, BytesRecv: 200})
	var end proto.SessionEnd
	browser.expect(proto.TypeSessionEnd, &end)
	if end.Reason != proto.EndAgentKill {
		t.Fatalf("end %+v", end)
	}
	var reason string
	var sent int64
	_ = e.pool.QueryRow(context.Background(), `SELECT state, end_reason, bytes_sent FROM sessions WHERE id = $1`, sid).Scan(&dbState, &reason, &sent)
	if dbState != "ended" || reason != proto.EndAgentKill || sent != 1000 {
		t.Fatalf("db after end: %s %s %d", dbState, reason, sent)
	}
	got := strings.Join(e.auditEvents(sid), ",")
	if got != "session.created,session.connected,session.ended" {
		t.Fatalf("audit = %s", got)
	}
	var meta map[string]any
	_ = e.pool.QueryRow(context.Background(), `SELECT meta FROM audit_events WHERE session_id = $1 AND event = 'session.connected'`, sid).Scan(&meta)
	if meta["path"] != "relay" || meta["agent_ip"] == "" || meta["client_ip"] == "" {
		t.Fatalf("connected meta %+v", meta)
	}

	// A new session is allowed now; the browser hanging up a pending session fails it.
	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r3", DeviceID: ag.id, ClientKeyID: keyID})
	browser.expect(proto.TypeSessionCreated, &created)
	browser.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: created.SessionID, Reason: proto.EndUserHangup})
	agent.expect(proto.TypeSessionEnd, &end)
	if end.SessionID != created.SessionID || end.Reason != proto.EndUserHangup {
		t.Fatalf("agent end %+v", end)
	}
	if got := strings.Join(e.auditEvents(created.SessionID), ","); got != "session.created,session.failed" {
		t.Fatalf("pending hangup audit = %s", got)
	}

	// The agent answers the browser's hangup with its own session.end carrying
	// byte counters; it updates the closed row without a second audit event.
	const trailingSent, trailingRecv = 4096, 512
	agent.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: created.SessionID, Reason: proto.EndUserHangup, BytesSent: trailingSent, BytesRecv: trailingRecv})
	var gotSent, gotRecv int64
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		_ = e.pool.QueryRow(context.Background(), `SELECT bytes_sent, bytes_recv FROM sessions WHERE id = $1`, created.SessionID).Scan(&gotSent, &gotRecv)
		if gotSent == trailingSent {
			break
		}
	}
	if gotSent != trailingSent || gotRecv != trailingRecv {
		t.Fatalf("trailing agent end bytes = %d/%d, want %d/%d", gotSent, gotRecv, trailingSent, trailingRecv)
	}
	if got := strings.Join(e.auditEvents(created.SessionID), ","); got != "session.created,session.failed" {
		t.Fatalf("trailing end changed audit = %s", got)
	}
}

func TestDeviceOwnershipAndAdminAuthz(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	ag := e.enroll(admin)
	bob := e.createUser(admin, "bob@example.com")

	for _, p := range []string{"/v1/admin/overview", "/v1/admin/users", "/v1/admin/devices", "/v1/admin/audit", "/v1/admin/settings"} {
		if st := e.call(bob, "GET", p, nil, nil); st != 403 {
			t.Errorf("non-admin GET %s = %d, want 403", p, st)
		}
	}
	if st := e.call(bob, "GET", "/v1/devices/"+ag.id, nil, nil); st != 404 {
		t.Fatalf("other user's device visible: %d", st)
	}
	var list struct{ Items []any }
	e.call(bob, "GET", "/v1/devices", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("bob sees %d devices", len(list.Items))
	}

	var ov map[string]any
	if st := e.call(admin, "GET", "/v1/admin/overview", nil, &ov); st != 200 {
		t.Fatalf("overview: %d", st)
	}
	for _, k := range []string{"server", "users", "devices", "sessions", "ws", "series", "recent_audit"} {
		if _, ok := ov[k]; !ok {
			t.Errorf("overview missing %q", k)
		}
	}
	if n := len(ov["series"].([]any)); n != 24 {
		t.Errorf("series has %d points, want 24", n)
	}

	var meResp struct{ User struct{ ID string } }
	e.call(admin, "GET", "/v1/auth/me", nil, &meResp)
	if st := e.call(admin, "PATCH", "/v1/admin/users/"+meResp.User.ID, map[string]any{"disabled": true}, nil); st != 400 {
		t.Fatalf("self-disable: %d, want 400", st)
	}
	var bobMe struct{ User struct{ ID string } }
	e.call(bob, "GET", "/v1/auth/me", nil, &bobMe)
	if st := e.call(admin, "PATCH", "/v1/admin/users/"+bobMe.User.ID, map[string]any{"disabled": true}, nil); st != 200 {
		t.Fatalf("disable bob: %d", st)
	}
	if st := e.call(bob, "GET", "/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("disabled user still signed in: %d", st)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE actor = 'admin' AND event = 'admin.user_updated'`).Scan(&n)
	if n != 1 {
		t.Fatalf("admin audit rows = %d, want 1", n)
	}

	// Admin disables the device: agent auth is refused.
	if st := e.call(admin, "PATCH", "/v1/admin/devices/"+ag.id, map[string]any{"disabled": true}, nil); st != 200 {
		t.Fatalf("disable device: %d", st)
	}
	var errBody struct {
		Error struct{ Code string } `json:"error"`
	}
	e.call(e.client(), "POST", "/v1/agent/challenge", map[string]string{"device_id": ag.id}, &errBody)
	if errBody.Error.Code != "device_disabled" {
		t.Fatalf("disabled device challenge: %+v", errBody)
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("expected the bootstrap audit row, got %d %v", n, err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE audit_events SET event = 'tampered'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE allowed: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM audit_events`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE allowed: %v", err)
	}
	var part string
	_ = e.pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM audit_events LIMIT 1`).Scan(&part)
	if want := "audit_events_" + time.Now().UTC().Format("200601"); part != want {
		t.Fatalf("row stored in partition %q, want %q", part, want)
	}
}

func TestBootSweepFailsPendingSessions(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	keyID := e.registerClientKey(c)
	ctx := context.Background()
	var uid, sid string
	_ = e.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, adminEmail).Scan(&uid)
	if err := e.pool.QueryRow(ctx, `INSERT INTO sessions (device_id, user_id, client_key_id) VALUES ($1, $2, $3) RETURNING id`,
		ag.id, uid, keyID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	// A second process starting against the same DB sweeps the stale row.
	a2, err := New(ctx, e.cfg, e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Argon: &cheapArgon})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	var state, reason string
	_ = e.pool.QueryRow(ctx, `SELECT state, end_reason FROM sessions WHERE id = $1`, sid).Scan(&state, &reason)
	if state != "failed" || reason != proto.EndServerRestart {
		t.Fatalf("after boot sweep: %s %s", state, reason)
	}
	if got := strings.Join(e.auditEvents(sid), ","); got != "session.failed" {
		t.Fatalf("audit = %s", got)
	}
}

func TestHelloReconcilesActiveSessions(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	keyID := e.registerClientKey(c)
	ctx := context.Background()
	var uid string
	_ = e.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, adminEmail).Scan(&uid)
	ids := make([]string, 2)
	for i := range ids {
		_ = e.pool.QueryRow(ctx, `INSERT INTO sessions (device_id, user_id, client_key_id, state, connected_at)
			VALUES ($1, $2, $3, 'active', now()) RETURNING id`, ag.id, uid, keyID).Scan(&ids[i])
	}
	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{OS: "darwin", Arch: "arm64", ActiveSessions: []string{ids[0]}})
	agent.expect(proto.TypeHelloAck, nil)
	var s0, s1 string
	_ = e.pool.QueryRow(ctx, `SELECT state FROM sessions WHERE id = $1`, ids[0]).Scan(&s0)
	_ = e.pool.QueryRow(ctx, `SELECT state FROM sessions WHERE id = $1`, ids[1]).Scan(&s1)
	if s0 != "active" || s1 != "ended" {
		t.Fatalf("claimed=%s unclaimed=%s, want active/ended", s0, s1)
	}
	if e.app.hub.ActiveCount() != 1 {
		t.Fatalf("live active sessions = %d, want 1", e.app.hub.ActiveCount())
	}
}

func TestFileAuditFromAgent(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	other := e.enroll(c)
	keyID := e.registerClientKey(c)
	browser := e.dialBrowser(c)
	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{OS: "linux", Arch: "amd64", ActiveSessions: []string{}})
	agent.expect(proto.TypeHelloAck, nil)

	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r1", DeviceID: ag.id, ClientKeyID: keyID})
	var created proto.SessionCreated
	browser.expect(proto.TypeSessionCreated, &created)
	sid := created.SessionID

	upload := proto.FileRecord{Xfer: 7, Direction: "upload", Name: "a (1).txt", Dir: "/home/device/Downloads", Size: 5, Bytes: 5,
		SHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", Result: "ok", DurationMS: 40}
	failed := proto.FileRecord{Xfer: 8, Direction: "download", Name: "big.iso", Dir: "/home/device", Size: 900, Bytes: 100,
		Result: "error", Error: "stalled", DurationMS: 60000}
	expectErr := func(w *wsClient, code string) {
		t.Helper()
		var er proto.Error
		w.expect(proto.TypeError, &er)
		if er.Code != code {
			t.Fatalf("error %+v, want %s", er, code)
		}
	}
	fileEvents := func() string {
		var out []string
		for _, ev := range e.auditEvents(sid) {
			if strings.HasPrefix(ev, "file.") {
				out = append(out, ev)
			}
		}
		return strings.Join(out, ",")
	}
	waitEvents := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && fileEvents() != want; time.Sleep(20 * time.Millisecond) {
		}
		if got := fileEvents(); got != want {
			t.Fatalf("file audit events = %q, want %q", got, want)
		}
	}

	// A pending session never connected: no file records yet.
	agent.send(proto.TypeSessionFile, proto.SessionFile{SessionID: sid, Files: []proto.FileRecord{upload}})
	expectErr(agent, proto.ErrNotFound)

	agent.send(proto.TypeSessionState, proto.SessionState{SessionID: sid, Connected: true, Path: proto.PathHost})
	browser.expect(proto.TypeSessionState, nil)

	invalid := upload
	invalid.Direction = "sideways"
	agent.send(proto.TypeSessionFile, proto.SessionFile{SessionID: sid, Files: []proto.FileRecord{upload, invalid, failed}})
	waitEvents("file.uploaded,file.download_failed")
	var meta map[string]any
	var actor string
	_ = e.pool.QueryRow(context.Background(), `SELECT actor, meta FROM audit_events WHERE session_id = $1 AND event = 'file.uploaded'`, sid).Scan(&actor, &meta)
	want := map[string]any{"name": "a (1).txt", "dir": "/home/device/Downloads", "size": float64(5), "bytes": float64(5),
		"sha256": upload.SHA256, "result": "ok", "error": "", "duration_ms": float64(40), "xfer": float64(7)}
	if actor != "agent" || fmt.Sprint(meta) != fmt.Sprint(want) {
		t.Fatalf("file.uploaded actor %s meta %v", actor, meta)
	}

	// Another device cannot write into this session's audit trail.
	stranger := e.dialAgent(other)
	stranger.send(proto.TypeSessionFile, proto.SessionFile{SessionID: sid, Files: []proto.FileRecord{upload}})
	expectErr(stranger, proto.ErrNotFound)

	tooMany := make([]proto.FileRecord, proto.MaxFileRecords+1)
	for i := range tooMany {
		tooMany[i] = upload
	}
	agent.send(proto.TypeSessionFile, proto.SessionFile{SessionID: sid, Files: tooMany})
	expectErr(agent, proto.ErrBadRequest)
	waitEvents("file.uploaded,file.download_failed")

	// Records flushed right after the session ended are still accepted.
	agent.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sid, Reason: proto.EndUserHangup})
	browser.expect(proto.TypeSessionEnd, nil)
	cancelled := proto.FileRecord{Xfer: 9, Direction: "upload", Name: "late.bin", Dir: "/tmp", Size: 10, Bytes: 2,
		Result: "cancelled", Error: "cancelled", DurationMS: 5}
	agent.send(proto.TypeSessionFile, proto.SessionFile{SessionID: sid, Files: []proto.FileRecord{cancelled}})
	waitEvents("file.uploaded,file.download_failed,file.upload_failed")

	// A session that failed before connecting stays closed to file records.
	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r2", DeviceID: ag.id, ClientKeyID: keyID})
	browser.expect(proto.TypeSessionCreated, &created)
	browser.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: created.SessionID, Reason: proto.EndUserHangup})
	agent.expect(proto.TypeSessionEnd, nil)
	agent.send(proto.TypeSessionFile, proto.SessionFile{SessionID: created.SessionID, Files: []proto.FileRecord{upload}})
	expectErr(agent, proto.ErrNotFound)
	agent.send(proto.TypeSessionFile, proto.SessionFile{SessionID: "not-a-uuid", Files: []proto.FileRecord{upload}})
	expectErr(agent, proto.ErrNotFound)
}

// "Allow and remember" on the device means the browser connects without a
// prompt from then on, unless the owner turns on "ask every time".
func TestRememberedBrowsersSkipPromptByDefault(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	var s struct {
		DefaultAutoAccept bool `json:"default_auto_accept"`
	}
	if st := e.call(c, "GET", "/v1/admin/settings", nil, &s); st != 200 || !s.DefaultAutoAccept {
		t.Fatalf("settings: %d default_auto_accept=%v, want true", st, s.DefaultAutoAccept)
	}
	ag := e.enroll(c)
	var d struct {
		Device struct {
			AutoAccept bool `json:"auto_accept"`
		} `json:"device"`
	}
	if st := e.call(c, "GET", "/v1/devices/"+ag.id, nil, &d); st != 200 || !d.Device.AutoAccept {
		t.Fatalf("device: %d auto_accept=%v, want true", st, d.Device.AutoAccept)
	}
}
