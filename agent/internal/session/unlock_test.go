package session

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/keymap"
	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/platform/fake"
	"github.com/ruolez/remotectl/proto"
)

func keyEvents(km keymap.Table, usages ...uint16) []platform.InputEvent {
	var out []platform.InputEvent
	for _, u := range usages {
		out = append(out, platform.InputEvent{Type: platform.EvKey, Keycode: km[u].Code, Flags: km[u].Flags, Down: true})
	}
	for i := len(usages) - 1; i >= 0; i-- {
		out = append(out, platform.InputEvent{Type: platform.EvKey, Keycode: km[usages[i]].Code, Flags: km[usages[i]].Flags, Down: false})
	}
	return out
}

func noSleep(time.Duration) bool { return true }

func TestRunUnlockTypesThePasswordOnTheLockScreen(t *testing.T) {
	const password = "pä$s ✓"
	chars := []string{"p", "ä", "$", "s", " ", "✓"}
	cases := []struct {
		goos      string
		km        keymap.Table
		hook      bool
		wantSAS   int
		wantEvent []platform.InputEvent
	}{
		{"darwin", keymap.Mac, false, 0,
			slices.Concat(keyEvents(keymap.Mac, usageLShift), keyEvents(keymap.Mac, usageLGUI, usageA), keyEvents(keymap.Mac, usageEnter))},
		{"windows", keymap.Windows, true, 1,
			slices.Concat(keyEvents(keymap.Windows, usageLCtrl, usageA), keyEvents(keymap.Windows, usageEnter))},
		{"windows", keymap.Windows, false, 0,
			slices.Concat(keyEvents(keymap.Windows, usageLCtrl), keyEvents(keymap.Windows, usageLCtrl, usageA), keyEvents(keymap.Windows, usageEnter))},
	}
	for _, c := range cases {
		t.Run(c.goos, func(t *testing.T) {
			b := fake.New()
			b.SetLocked(true)
			sas := 0
			var hook func()
			if c.hook {
				hook = func() { sas++ }
			}
			if err := runUnlock(b, c.km, hook, unlockSteps(c.goos, password), noSleep); err != nil {
				t.Fatal(err)
			}
			if got := b.Texts(); !slices.Equal(got, chars) {
				t.Errorf("typed %q, want one character at a time %q", got, chars)
			}
			if got := b.Injected(); sas != c.wantSAS || !slices.Equal(got, c.wantEvent) {
				t.Errorf("secure attention %d (want %d), keys %+v, want %+v", sas, c.wantSAS, got, c.wantEvent)
			}
		})
	}
}

func TestRunUnlockTypesNothingOnceTheScreenUnlocks(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			b := fake.New()
			b.SetLocked(true)
			unlockDuringWait := func(time.Duration) bool { b.SetLocked(false); return true }
			err := runUnlock(b, keymap.For(goos), func() {}, unlockSteps(goos, "secret"), unlockDuringWait)
			if err != errUnlockNotLocked || len(b.Texts()) != 0 {
				t.Fatalf("err = %v, typed %q: want nothing typed on an unlocked desktop", err, b.Texts())
			}
		})
	}
}

// unlockFixture is a manager that can unlock, a pinned browser key and a
// session of its user.
type unlockFixture struct {
	backend *fake.Backend
	pins    *pins.Store
	mgr     *Manager
	client  *ecdsa.PrivateKey
	spki    []byte
	sess    *sess
}

func newUnlockFixture(t *testing.T) *unlockFixture {
	t.Helper()
	_, dev, _ := ed25519.GenerateKey(rand.Reader)
	ps, err := pins.Open(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	b := fake.New()
	b.SetLocked(true)
	m, err := NewManager(Options{Backend: b, DeviceKey: dev, Pins: ps, Send: func(string, any) error { return nil },
		Keymap: keymap.Mac, UnlockOS: "darwin", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	m.sleep = func(context.Context, time.Duration) bool { return true }
	t.Cleanup(m.Close)
	ck, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&ck.PublicKey)
	if err := ps.Pin(base64.StdEncoding.EncodeToString(der), "Chrome on macOS", ownerID, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := &sess{m: m, userID: ownerID, ctx: context.Background(), log: m.o.Log}
	return &unlockFixture{backend: b, pins: ps, mgr: m, client: ck, spki: der, sess: s}
}

func signWith(k *ecdsa.PrivateKey) func([]byte) ([]byte, error) {
	return func(msg []byte) ([]byte, error) {
		h := sha256.Sum256(msg)
		r, s, err := ecdsa.Sign(rand.Reader, k, h[:])
		if err != nil {
			return nil, err
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return sig, nil
	}
}

func (f *unlockFixture) seal(t *testing.T, unlockSPKI []byte, client *ecdsa.PrivateKey, userID, password string) proto.SealedCredential {
	t.Helper()
	der, _ := x509.MarshalPKIXPublicKey(&client.PublicKey)
	s, err := proto.SealCredential(rand.Reader, unlockSPKI, der, userID, proto.Credential{Username: "alice", Password: password}, signWith(client))
	if err != nil {
		t.Fatal(err)
	}
	return *s
}

func TestUnlockOpensOnlyCredentialsItShould(t *testing.T) {
	stranger, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDevice := newUnlockFixture(t)
	cases := []struct {
		name   string
		locked bool
		seal   func(f *unlockFixture) proto.SealedCredential
		revoke bool
		want   string
	}{
		{"a pinned browser's credential on the lock screen", true,
			func(f *unlockFixture) proto.SealedCredential {
				return f.seal(t, f.mgr.unlockSPKI, f.client, ownerID, "secret")
			}, false, ""},
		{"the desktop is not locked", false,
			func(f *unlockFixture) proto.SealedCredential {
				return f.seal(t, f.mgr.unlockSPKI, f.client, ownerID, "secret")
			}, false, unlockNotLocked},
		{"a browser the device never approved", true,
			func(f *unlockFixture) proto.SealedCredential {
				return f.seal(t, f.mgr.unlockSPKI, stranger, ownerID, "secret")
			}, false, unlockUntrusted},
		{"a revoked browser", true,
			func(f *unlockFixture) proto.SealedCredential {
				return f.seal(t, f.mgr.unlockSPKI, f.client, ownerID, "secret")
			}, true, unlockUntrusted},
		{"sealed for another user", true,
			func(f *unlockFixture) proto.SealedCredential {
				return f.seal(t, f.mgr.unlockSPKI, f.client, "someone-else", "secret")
			}, false, unlockBadCredential},
		{"sealed to another device's key", true,
			func(f *unlockFixture) proto.SealedCredential {
				return f.seal(t, otherDevice.mgr.unlockSPKI, f.client, ownerID, "secret")
			}, false, unlockStaleKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newUnlockFixture(t)
			f.backend.SetLocked(c.locked)
			if c.revoke {
				f.mgr.Revoke(base64.StdEncoding.EncodeToString(f.spki))
			}
			got := f.sess.unlock1(c.seal(f))
			typed := strings.Join(f.backend.Texts(), "")
			wantTyped := ""
			if c.want == "" {
				wantTyped = "secret"
			}
			if got != c.want || typed != wantTyped {
				t.Fatalf("code %q typed %q, want code %q typed %q", got, typed, c.want, wantTyped)
			}
		})
	}
}

func TestUnlockKeyIsSignedByTheDeviceKeyAndStable(t *testing.T) {
	_, dev, _ := ed25519.GenerateKey(rand.Reader)
	mk := func() *Manager {
		m, err := NewManager(Options{Backend: fake.New(), DeviceKey: dev, Send: func(string, any) error { return nil }, UnlockOS: "windows"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m
	}
	a, b := mk(), mk()
	if string(a.unlockSPKI) != string(b.unlockSPKI) {
		t.Fatal("the unlock key changed between starts: saved credentials would stop opening")
	}
	hello := map[string]any{}
	a.helloUnlock(hello)
	spki, _ := base64.StdEncoding.DecodeString(hello["unlock_key"].(string))
	msg := proto.UnlockKeyMessage(proto.SPKIHash(spki))
	pub := base64.StdEncoding.EncodeToString(dev.Public().(ed25519.PublicKey))
	if err := proto.VerifyDeviceSig(pub, hello["unlock_key_sig"].(string), msg); err != nil {
		t.Fatalf("unlock key signature: %v", err)
	}

	off, _ := NewManager(Options{Backend: fake.New(), DeviceKey: dev, Send: func(string, any) error { return nil }})
	t.Cleanup(off.Close)
	none := map[string]any{}
	off.helloUnlock(none)
	if len(none) != 0 || slices.Contains(helloFeatures(off.o, false), proto.FeatureUnlock) {
		t.Fatalf("a device without UnlockOS offers unlock: %v", none)
	}
}

func TestLoopbackUnlockOverTheControlChannel(t *testing.T) {
	h := newHarness(t, platform.ConsentRemember, func(o *Options) { o.UnlockOS = "darwin"; o.Keymap = keymap.Mac })
	h.mgr.sleep = func(context.Context, time.Duration) bool { return true }
	h.backend.SetLocked(true)
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	hello := h.waitControl(t, "hello")
	if !slices.Contains(hello["features"].([]any), any(proto.FeatureUnlock)) || hello["locked"] != true {
		t.Fatalf("hello features=%v locked=%v", hello["features"], hello["locked"])
	}
	spki, _ := base64.StdEncoding.DecodeString(hello["unlock_key"].(string))
	f := &unlockFixture{}
	cred := f.seal(t, spki, h.clientK, ownerID, "hunter2")
	send := func(id int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"t": "unlock", "id": id, "credential": cred})
		if err := h.control.SendText(string(b)); err != nil {
			t.Fatal(err)
		}
		for {
			if m := h.waitControl(t, "unlock"); m["id"] == float64(id) {
				return m
			}
		}
	}
	if m := send(1); m["ok"] != true {
		t.Fatalf("unlock = %v", m)
	}
	if typed := strings.Join(h.backend.Texts(), ""); typed != "hunter2" {
		t.Fatalf("typed %q", typed)
	}
	if m := send(2); m["ok"] != false || m["code"] != unlockTooSoon {
		t.Fatalf("a second attempt right away = %v, want too_soon", m)
	}
}
