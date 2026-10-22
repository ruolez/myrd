package preview

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/platform/fake"
	"github.com/ruolez/remotectl/proto"
)

const deviceID = "3f0c6c1e-6c55-4c3a-9a39-0a3f8d0e2b11"

func TestDeviceReason(t *testing.T) {
	ok := DeviceState{ScreenPermission: true}
	cases := []struct {
		name string
		s    DeviceState
		want string
	}{
		{"all clear", ok, ""},
		{"switched off wins over everything", DeviceState{PreviewsOff: true, Paused: true}, proto.PreviewOff},
		{"paused", DeviceState{Paused: true, ScreenPermission: true}, proto.PreviewPaused},
		{"updating", DeviceState{Updating: true, ScreenPermission: true}, proto.PreviewUpdating},
		{"ask every time", DeviceState{AskEveryTime: true, ScreenPermission: true}, proto.PreviewAskEveryTime},
		{"no permission", DeviceState{}, proto.PreviewNoPermission},
		{"in session", DeviceState{InSession: true, ScreenPermission: true}, proto.PreviewInSession},
	}
	for _, c := range cases {
		if got := deviceReason(c.s); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestKeyReason(t *testing.T) {
	cases := []struct {
		k    KeyState
		want string
	}{
		{KeyState{Pinned: true, KeySigOK: true}, ""},
		{KeyState{Pinned: false, KeySigOK: true}, proto.PreviewUntrusted},
		{KeyState{Revoked: true, Pinned: true, KeySigOK: true}, proto.PreviewUntrusted},
		{KeyState{Pinned: true, KeySigOK: false}, proto.PreviewBadKey},
	}
	for i, c := range cases {
		if got := keyReason(c.k); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestEncodeFitsBudgetEvenForNoise(t *testing.T) {
	img := platform.Image{Width: 480, Height: 270, Stride: 480 * 4, Pix: make([]byte, 480*270*4)}
	_, _ = rand.Read(img.Pix)
	jpg, w, h, err := encodeJPEG(img, proto.MaxPreviewCiphertext-16)
	if err != nil || len(jpg) > proto.MaxPreviewCiphertext-16 {
		t.Fatalf("noise: %d bytes, %v", len(jpg), err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
	if err != nil || cfg.Width != w || cfg.Height != h || max(w, h) > MaxSide {
		t.Fatalf("decoded %+v (%v), reported %dx%d", cfg, err, w, h)
	}
}

// browser is a test browser: a client key pinned (or not) on the device and a preview key.
type browser struct {
	id      string
	spki    string
	preview *ecdh.PrivateKey
	pkSPKI  []byte
	sig     string
}

func newBrowser(t *testing.T, id string) *browser {
	t.Helper()
	ck, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&ck.PublicKey)
	pk, _ := ecdh.P256().GenerateKey(rand.Reader)
	pub, _ := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pk.PublicKey().Bytes())
	pkDER, _ := x509.MarshalPKIXPublicKey(pub)
	h := sha256.Sum256(proto.PreviewKeyMessage(proto.SPKIHash(der), proto.SPKIHash(pkDER)))
	r, s, _ := ecdsa.Sign(rand.Reader, ck, h[:])
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return &browser{id: id, spki: base64.StdEncoding.EncodeToString(der), preview: pk, pkSPKI: pkDER,
		sig: base64.StdEncoding.EncodeToString(sig)}
}

func (b *browser) recipient(user string) proto.PreviewRecipient {
	return proto.PreviewRecipient{ClientKey: proto.ClientKeyInfo{ID: b.id, PublicKey: b.spki, Label: "Chrome " + b.id},
		PreviewKey: base64.StdEncoding.EncodeToString(b.pkSPKI), PreviewKeySig: b.sig, User: proto.UserInfo{ID: user}}
}

type sentMsg struct {
	typ string
	msg any
}

type harness struct {
	svc     *Service
	backend *fake.Backend
	pins    *pins.Store
	devPub  ed25519.PublicKey
	state   DeviceState
	now     time.Time
	mu      sync.Mutex
	out     []sentMsg
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ps, err := pins.Open(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	h := &harness{backend: fake.New(), pins: ps, devPub: pub, state: DeviceState{ScreenPermission: true},
		now: time.Unix(1_800_000_000, 0)}
	h.svc = New(Options{Backend: h.backend, Pins: ps, DeviceKey: priv, DeviceID: func() string { return deviceID },
		State: func() DeviceState { return h.state }, Now: func() time.Time { return h.now },
		Send: func(typ string, msg any) error {
			h.mu.Lock()
			h.out = append(h.out, sentMsg{typ, msg})
			h.mu.Unlock()
			return nil
		}})
	return h
}

func (h *harness) take() []sentMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.out
	h.out = nil
	return out
}

func (h *harness) open(t *testing.T, b *browser, m sentMsg) []byte {
	t.Helper()
	p, ok := m.msg.(proto.Preview)
	if m.typ != proto.TypePreview || !ok {
		t.Fatalf("got %s %+v, want a preview", m.typ, m.msg)
	}
	sealed, err := p.Sealed()
	if err != nil {
		t.Fatal(err)
	}
	ck, _ := base64.StdEncoding.DecodeString(b.spki)
	pt, err := proto.OpenPreview(h.devPub, b.preview, p.Meta(deviceID, proto.SPKIHash(ck), proto.SPKIHash(b.pkSPKI)), sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return pt
}

func reasons(msgs []sentMsg) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		if u, ok := m.msg.(proto.PreviewUnavailable); ok {
			out[u.ClientKeyID] = u.Reason
		}
	}
	return out
}

func TestServeSealsForPinnedBrowsersOnly(t *testing.T) {
	h := newHarness(t)
	pinned, other, forged := newBrowser(t, "k1"), newBrowser(t, "k2"), newBrowser(t, "k3")
	forged.sig = other.sig // a preview key the client key never vouched for
	for _, b := range []*browser{pinned, forged} {
		if err := h.pins.Pin(b.spki, "x", "u1", h.now); err != nil {
			t.Fatal(err)
		}
	}
	// "other" belongs to the same account but was never approved on the device itself.
	h.svc.Serve(proto.PreviewRequest{Req: "r1", Recipients: []proto.PreviewRecipient{
		pinned.recipient("u1"), other.recipient("u1"), forged.recipient("u1"), pinned.recipient("someone-else")}})
	msgs := h.take()
	jpg := h.open(t, pinned, msgs[len(msgs)-1])
	if _, err := jpeg.DecodeConfig(bytes.NewReader(jpg)); err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	got := reasons(msgs)
	if got["k2"] != proto.PreviewUntrusted || got["k3"] != proto.PreviewBadKey || len(msgs) != 4 {
		b, _ := json.Marshal(msgs)
		t.Fatalf("reasons %v (messages %s)", got, b)
	}
	if h.backend.Snapshots() != 1 {
		t.Fatalf("snapshots %d, want 1 for the whole request", h.backend.Snapshots())
	}
}

func TestServeReusesARecentPictureAndRateLimitsEachKey(t *testing.T) {
	h := newHarness(t)
	a, b := newBrowser(t, "a"), newBrowser(t, "b")
	_ = h.pins.Pin(a.spki, "a", "u", h.now)
	_ = h.pins.Pin(b.spki, "b", "u", h.now)
	h.svc.Serve(proto.PreviewRequest{Req: "1", Recipients: []proto.PreviewRecipient{a.recipient("u")}})
	h.take()

	h.now = h.now.Add(10 * time.Second)
	h.svc.Serve(proto.PreviewRequest{Req: "2", Recipients: []proto.PreviewRecipient{a.recipient("u"), b.recipient("u")}})
	msgs := h.take()
	if r := reasons(msgs)["a"]; r != proto.PreviewTooSoon {
		t.Fatalf("a asked again after 10 s: %q", r)
	}
	h.open(t, b, msgs[len(msgs)-1])
	if h.backend.Snapshots() != 1 {
		t.Fatalf("a 10 s old picture should be reused: %d snapshots", h.backend.Snapshots())
	}

	h.now = h.now.Add(ReuseWithin + MinInterval)
	h.svc.Serve(proto.PreviewRequest{Req: "3", Recipients: []proto.PreviewRecipient{a.recipient("u")}})
	h.open(t, a, h.take()[0])
	if h.backend.Snapshots() != 2 {
		t.Fatalf("a stale picture should be retaken: %d snapshots", h.backend.Snapshots())
	}
}

func TestServeReportsWhyNot(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*harness)
		want  string
	}{
		{"previews off", func(h *harness) { h.state.PreviewsOff = true }, proto.PreviewOff},
		{"ask every time", func(h *harness) { h.state.AskEveryTime = true }, proto.PreviewAskEveryTime},
		{"locked", func(h *harness) { h.backend.SetLocked(true) }, proto.PreviewLocked},
		{"asleep", func(h *harness) { h.backend.SetSnapshot(platform.Image{}, platform.ErrAsleep) }, proto.PreviewDisplayOff},
		{"capturing", func(h *harness) { h.backend.SetSnapshot(platform.Image{}, platform.ErrBusy) }, proto.PreviewInSession},
		{"unsupported", func(h *harness) { h.backend.SetSnapshot(platform.Image{}, platform.ErrUnsupported) }, proto.PreviewUnsupported},
	}
	for _, c := range cases {
		h := newHarness(t)
		b := newBrowser(t, "k")
		_ = h.pins.Pin(b.spki, "k", "u", h.now)
		c.setup(h)
		h.svc.Serve(proto.PreviewRequest{Req: "r", Recipients: []proto.PreviewRecipient{b.recipient("u")}})
		msgs := h.take()
		if len(msgs) != 1 || reasons(msgs)[""] != c.want {
			t.Errorf("%s: %+v, want one %q for every recipient", c.name, msgs, c.want)
		}
	}
}
