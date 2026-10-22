package app

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// previewBrowser is a browser with a client key and a registered preview key.
type previewBrowser struct {
	keyID   string
	spki    []byte
	priv    *ecdsa.PrivateKey
	preview *ecdh.PrivateKey
	pkSPKI  []byte
}

func p1363(t *testing.T, k *ecdsa.PrivateKey, msg []byte) string {
	t.Helper()
	h := sha256.Sum256(msg)
	r, s, err := ecdsa.Sign(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return base64.StdEncoding.EncodeToString(sig)
}

func (e *env) previewBrowser(c *http.Client) *previewBrowser {
	e.t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	var out struct {
		ClientKey struct{ ID string } `json:"client_key"`
	}
	if st := e.call(c, "POST", "/v1/client-keys", map[string]string{"public_key": base64.StdEncoding.EncodeToString(der), "label": "Chrome"}, &out); st != 200 {
		e.t.Fatalf("client key: %d", st)
	}
	pk, _ := ecdh.P256().GenerateKey(rand.Reader)
	pub, _ := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pk.PublicKey().Bytes())
	pkDER, _ := x509.MarshalPKIXPublicKey(pub)
	b := &previewBrowser{keyID: out.ClientKey.ID, spki: der, priv: k, preview: pk, pkSPKI: pkDER}
	sig := p1363(e.t, k, proto.PreviewKeyMessage(proto.SPKIHash(der), proto.SPKIHash(pkDER)))
	if st := e.call(c, "PUT", "/v1/client-keys/"+b.keyID+"/preview-key",
		map[string]string{"public_key": base64.StdEncoding.EncodeToString(pkDER), "signature": sig}, nil); st != 200 {
		e.t.Fatalf("preview key: %d", st)
	}
	return b
}

// previewAgent connects a device that takes previews and has pinned keys.
func (e *env) previewAgent(ag *fakeAgent, pinned ...*previewBrowser) *wsClient {
	e.t.Helper()
	agent := e.dialAgent(ag)
	var pins []proto.PinnedClient
	for _, b := range pinned {
		pins = append(pins, proto.PinnedClient{PublicKeySHA256: proto.SPKIHash(b.spki), Label: "Chrome"})
	}
	agent.send(proto.TypeHello, proto.Hello{OS: "linux", Arch: "amd64", ActiveSessions: []string{}, PinnedClientKeys: pins,
		Capabilities: proto.Capabilities{Features: []string{proto.FeaturePreview}}})
	agent.expect(proto.TypeHelloAck, nil)
	return agent
}

// requestNow makes every watched device due and runs one scheduler pass.
func (e *env) requestNow() {
	p := e.app.hub.prev
	p.mu.Lock()
	for id := range p.next {
		p.next[id] = time.Time{}
	}
	p.mu.Unlock()
	e.app.hub.previewTick()
}

func seal(t *testing.T, ag *fakeAgent, b *previewBrowser, req string, jpeg []byte) proto.Preview {
	t.Helper()
	m := proto.PreviewMeta{DeviceID: ag.id, CKSHA: proto.SPKIHash(b.spki), PKSHA: proto.SPKIHash(b.pkSPKI),
		CapturedAtMS: time.Now().UnixMilli(), Width: 480, Height: 270, Mime: proto.PreviewMime}
	s, err := proto.SealPreview(rand.Reader, ag.priv, b.pkSPKI, m, jpeg)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	return proto.Preview{Req: req, ClientKeyID: b.keyID, CapturedAtMS: m.CapturedAtMS, Width: 480, Height: 270, Mime: m.Mime,
		EphPub: b64(s.EphPub), IV: b64(s.IV), Ciphertext: b64(s.Ciphertext), Sig: b64(s.Sig)}
}

func TestPreviewKeyRegistration(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	b := e.previewBrowser(c)
	var keys struct {
		Items []struct {
			ID         string  `json:"id"`
			PreviewKey *string `json:"preview_key"`
		} `json:"items"`
	}
	e.call(c, "GET", "/v1/client-keys", nil, &keys)
	if len(keys.Items) != 1 || keys.Items[0].PreviewKey == nil || *keys.Items[0].PreviewKey != base64.StdEncoding.EncodeToString(b.pkSPKI) {
		t.Fatalf("client keys %+v", keys.Items)
	}
	// A preview key the client key did not sign is refused.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	forged := p1363(t, other, proto.PreviewKeyMessage(proto.SPKIHash(b.spki), proto.SPKIHash(b.pkSPKI)))
	if st := e.call(c, "PUT", "/v1/client-keys/"+b.keyID+"/preview-key",
		map[string]string{"public_key": base64.StdEncoding.EncodeToString(b.pkSPKI), "signature": forged}, nil); st != 400 {
		t.Fatalf("forged signature: %d, want 400", st)
	}
	stranger := e.createUser(c, "s@example.com")
	if st := e.call(stranger, "PUT", "/v1/client-keys/"+b.keyID+"/preview-key",
		map[string]string{"public_key": base64.StdEncoding.EncodeToString(b.pkSPKI), "signature": forged}, nil); st != 404 {
		t.Fatalf("another user's key: %d, want 404", st)
	}
}

func TestPreviewWatchFlow(t *testing.T) {
	e := newEnv(t)
	e.app.hub.prev.jitter = 0
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	b := e.previewBrowser(c)
	agent := e.previewAgent(ag, b)
	browser := e.dialBrowser(c)
	browser.send(proto.TypePreviewWatch, proto.PreviewWatch{ClientKeyID: b.keyID, DeviceIDs: []string{ag.id}})
	browser.send(proto.TypePing, proto.Ping{TS: 1})
	browser.expect(proto.TypePong, nil) // the watch is registered

	e.requestNow()
	var req proto.PreviewRequest
	agent.expect(proto.TypePreviewRequest, &req)
	if len(req.Recipients) != 1 || req.Recipients[0].ClientKey.ID != b.keyID ||
		req.Recipients[0].PreviewKey != base64.StdEncoding.EncodeToString(b.pkSPKI) || req.Recipients[0].User.Email != adminEmail {
		t.Fatalf("request %+v", req)
	}
	// The agent can check the browser vouched for the preview key.
	if err := proto.VerifyBrowserSig(req.Recipients[0].ClientKey.PublicKey, req.Recipients[0].PreviewKeySig,
		proto.PreviewKeyMessage(proto.SPKIHash(b.spki), proto.SPKIHash(b.pkSPKI))); err != nil {
		t.Fatalf("preview key signature: %v", err)
	}

	jpeg := []byte("\xff\xd8 the screen \xff\xd9")
	agent.send(proto.TypePreview, seal(t, ag, b, req.Req, jpeg))
	var got proto.Preview
	browser.expect(proto.TypePreview, &got)
	sealed, _ := got.Sealed()
	pt, err := proto.OpenPreview(ag.priv.Public().(ed25519.PublicKey), b.preview, got.Meta(ag.id, proto.SPKIHash(b.spki), proto.SPKIHash(b.pkSPKI)), sealed)
	if err != nil || string(pt) != string(jpeg) || got.DeviceID != ag.id || got.ReceivedAt == "" || got.Req != "" {
		t.Fatalf("relayed preview %+v: %q %v", got, pt, err)
	}

	// The dashboard's first paint gets it over REST.
	var list struct{ Items []proto.Preview }
	if st := e.call(c, "GET", "/v1/previews?client_key_id="+b.keyID+"&device_ids="+ag.id, nil, &list); st != 200 ||
		len(list.Items) != 1 || list.Items[0].Ciphertext != got.Ciphertext {
		t.Fatalf("GET previews: %d %+v", st, list.Items)
	}
	var stored int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM device_previews WHERE device_id = $1`, ag.id).Scan(&stored)
	if stored != 1 {
		t.Fatalf("stored previews %d", stored)
	}
	if !strings.Contains(strings.Join(e.deviceEvents(ag.id), ","), "preview.served") {
		t.Fatal("no preview.served audit event")
	}

	// A reply nobody asked for, and a forged one, are refused and not relayed.
	agent.send(proto.TypePreview, seal(t, ag, b, "pv-unknown", jpeg))
	var er proto.Error
	agent.expect(proto.TypeError, &er)
	if er.Code != proto.ErrNotFound {
		t.Fatalf("unrequested preview: %+v", er)
	}
	e.requestNow()
	agent.expect(proto.TypePreviewRequest, &req)
	forged := seal(t, ag, b, req.Req, jpeg)
	forged.Width = 481 // not what was signed
	agent.send(proto.TypePreview, forged)
	agent.expect(proto.TypeError, &er)
	if er.Code != proto.ErrBadRequest {
		t.Fatalf("forged preview: %+v", er)
	}

	// Unavailable reasons reach the browser; too_soon only reschedules.
	e.requestNow()
	agent.expect(proto.TypePreviewRequest, &req)
	agent.send(proto.TypePreviewUnavailable, proto.PreviewUnavailable{Req: req.Req, Reason: proto.PreviewLocked})
	var un proto.PreviewUnavailable
	browser.expect(proto.TypePreviewUnavailable, &un)
	if un.DeviceID != ag.id || un.Reason != proto.PreviewLocked {
		t.Fatalf("unavailable %+v", un)
	}
}

func TestPreviewGates(t *testing.T) {
	e := newEnv(t)
	e.app.hub.prev.jitter = 0
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	pinned, unpinned := e.previewBrowser(c), e.previewBrowser(c)
	agent := e.previewAgent(ag, pinned)
	browser := e.dialBrowser(c)
	expectNone := func(why string) {
		t.Helper()
		e.requestNow()
		agent.send(proto.TypePing, proto.Ping{TS: 7})
		agent.expectBefore(proto.TypePong, proto.TypePreviewRequest, why)
	}

	// A browser key the device has not pinned is not asked for.
	browser.send(proto.TypePreviewWatch, proto.PreviewWatch{ClientKeyID: unpinned.keyID, DeviceIDs: []string{ag.id}})
	browser.send(proto.TypePing, proto.Ping{TS: 1})
	browser.expect(proto.TypePong, nil)
	expectNone("key not pinned on the device")
	var un proto.PreviewUnavailable
	browser.expect(proto.TypePreviewUnavailable, &un)
	if un.Reason != proto.PreviewUntrusted || un.DeviceID != ag.id {
		t.Fatalf("unpinned browser told %+v", un)
	}

	browser.send(proto.TypePreviewWatch, proto.PreviewWatch{ClientKeyID: pinned.keyID, DeviceIDs: []string{ag.id}})
	browser.send(proto.TypePing, proto.Ping{TS: 2})
	browser.expect(proto.TypePong, nil)

	// The owner's per-device switch.
	if st := e.call(c, "PATCH", "/v1/devices/"+ag.id, map[string]any{"previews": false}, nil); st != 200 {
		t.Fatalf("previews off: %d", st)
	}
	expectNone("device previews off")
	e.call(c, "PATCH", "/v1/devices/"+ag.id, map[string]any{"previews": true}, nil)

	// The admin switch.
	var s map[string]any
	e.call(c, "GET", "/v1/admin/settings", nil, &s)
	s["screen_previews"] = false
	if st := e.call(c, "PUT", "/v1/admin/settings", s, nil); st != 200 {
		t.Fatalf("admin off: %d", st)
	}
	expectNone("admin turned previews off")
	s["screen_previews"] = true
	e.call(c, "PUT", "/v1/admin/settings", s, nil)

	// An agent without the feature.
	old := e.dialAgent(ag)
	old.send(proto.TypeHello, proto.Hello{OS: "linux", Arch: "amd64", ActiveSessions: []string{}})
	old.expect(proto.TypeHelloAck, nil)
	e.requestNow()
	old.send(proto.TypePing, proto.Ping{TS: 3})
	old.expectBefore(proto.TypePong, proto.TypePreviewRequest, "agent without the preview feature")

	// Back to a capable agent: asked.
	agent = e.previewAgent(ag, pinned)
	e.requestNow()
	agent.expect(proto.TypePreviewRequest, nil)
}
