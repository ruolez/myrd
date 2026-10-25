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
	"sync"
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

// testClock is a clock that moves only when the code under test sleeps.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)} }

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) sleep(d time.Duration) bool {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return true
}

func (c *testClock) sleepCtx(_ context.Context, d time.Duration) bool { return c.sleep(d) }

func repeat(p platform.UnlockPrompt, n int) []platform.UnlockPrompt {
	return slices.Repeat([]platform.UnlockPrompt{p}, n)
}

// centerClick is the click that lifts the Windows lock curtain: the middle of
// the fake backend's primary monitor.
var centerClick = []platform.InputEvent{
	{Type: platform.EvMouseMove, MonitorID: 1, X: 32768, Y: 32768},
	{Type: platform.EvMouseButton, Button: platform.BtnLeft, Down: true},
	{Type: platform.EvMouseButton, Button: platform.BtnLeft, Down: false},
}

type unlockRun struct {
	backend *fake.Backend
	sas     int
	trace   unlockTrace
	err     error
}

// runUnlockOn unlocks a locked fake device of goos whose UnlockPrompt
// returns prompts, the password field last raised at raisedAt (zero: never).
func runUnlockOn(t *testing.T, goos string, prompts []platform.UnlockPrompt, raisedAt func(now time.Time) time.Time) unlockRun {
	t.Helper()
	b := fake.New()
	b.SetLocked(true)
	b.SetUnlockPrompt(prompts...)
	clk := newTestClock()
	r := unlockRun{backend: b}
	u := &unlocker{b: b, km: keymap.For(goos), sas: func() { r.sas++ }, sleep: clk.sleep, now: clk.now}
	var raised time.Time
	if raisedAt != nil {
		raised = raisedAt(clk.now())
	}
	r.trace, r.err = u.run(unlockPlanFor(goos), "secret", raised)
	return r
}

func TestUnlockTypesThePasswordOnTheLockScreen(t *testing.T) {
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
			b.SetUnlockPrompt(platform.PromptHidden, platform.PromptReady)
			sas := 0
			var hook func()
			if c.hook {
				hook = func() { sas++ }
			}
			clk := newTestClock()
			u := &unlocker{b: b, km: c.km, sas: hook, sleep: clk.sleep, now: clk.now}
			if _, err := u.run(unlockPlanFor(c.goos), password, time.Time{}); err != nil {
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

func TestUnlockTypesAsSoonAsThePasswordFieldTakesKeys(t *testing.T) {
	ms := time.Millisecond
	fresh := func(now time.Time) time.Time { return now.Add(-time.Second) }
	cases := []struct {
		name     string
		goos     string
		prompts  []platform.UnlockPrompt
		raisedAt func(time.Time) time.Time
		wantSAS  int
		wantWait time.Duration
	}{
		{"windows sign-in box already up: no Ctrl+Alt+Delete", "windows",
			[]platform.UnlockPrompt{platform.PromptReady}, nil, 0, 100 * ms},
		{"windows sign-in box comes up after Ctrl+Alt+Delete", "windows",
			[]platform.UnlockPrompt{platform.PromptHidden, platform.PromptHidden, platform.PromptReady}, nil, 1, 200 * ms},
		{"windows box raised when the session opened", "windows",
			[]platform.UnlockPrompt{platform.PromptReady}, fresh, 0, 0},
		{"macOS display awake: Shift, then a short settle", "darwin",
			[]platform.UnlockPrompt{platform.PromptReady}, nil, 0, 300 * ms},
		{"macOS display waking up", "darwin",
			[]platform.UnlockPrompt{platform.PromptHidden, platform.PromptHidden, platform.PromptReady}, nil, 0, 400 * ms},
		{"windows without UI Automation: the old fixed wait, after a click", "windows",
			nil, nil, 1, 2700 * ms},
		{"macOS without the display state: the old fixed wait", "darwin", nil, nil, 0, time.Second},
		{"linux types right away", "linux", nil, nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runUnlockOn(t, c.goos, c.prompts, c.raisedAt)
			if r.err != nil || strings.Join(r.backend.Texts(), "") != "secret" {
				t.Fatalf("err %v, typed %q", r.err, r.backend.Texts())
			}
			if r.sas != c.wantSAS || r.trace.waited != c.wantWait {
				t.Fatalf("secure attention %d, first key after %v; want %d after %v", r.sas, r.trace.waited, c.wantSAS, c.wantWait)
			}
		})
	}
}

func TestUnlockClicksAwayALockCurtainCtrlAltDeleteLeftUp(t *testing.T) {
	r := runUnlockOn(t, "windows", append(repeat(platform.PromptHidden, 9), platform.PromptReady), nil)
	km := keymap.Windows
	want := slices.Concat(centerClick, keyEvents(km, usageLCtrl, usageA), keyEvents(km, usageEnter))
	if r.err != nil || !slices.Equal(r.backend.Injected(), want) || !r.trace.clicked {
		t.Fatalf("err %v, events %+v; want a click in the middle of the screen, then the password", r.err, r.backend.Injected())
	}
	if r.trace.waited != 900*time.Millisecond {
		t.Fatalf("first key after %v, want 900ms (click at 700ms, box up at 800ms, 100ms settle)", r.trace.waited)
	}
}

func TestUnlockTypesNothingWhenThePasswordFieldNeverAppears(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			r := runUnlockOn(t, goos, []platform.UnlockPrompt{platform.PromptHidden}, nil)
			for _, ev := range r.backend.Injected() {
				if ev.Type == platform.EvKey && ev.Keycode == keymap.For(goos)[usageEnter].Code {
					t.Fatalf("pressed Enter with no password field: %+v", r.backend.Injected())
				}
			}
			if r.err != errUnlockNoPrompt || len(r.backend.Texts()) != 0 {
				t.Fatalf("err %v, typed %q; want errUnlockNoPrompt and nothing typed", r.err, r.backend.Texts())
			}
		})
	}
}

func TestUnlockTypesNothingOnceTheScreenUnlocks(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			b := fake.New()
			b.SetLocked(true)
			b.SetUnlockPrompt(platform.PromptHidden)
			clk := newTestClock()
			unlockDuringWait := func(d time.Duration) bool { b.SetLocked(false); return clk.sleep(d) }
			u := &unlocker{b: b, km: keymap.For(goos), sas: func() {}, sleep: unlockDuringWait, now: clk.now}
			_, err := u.run(unlockPlanFor(goos), "secret", time.Time{})
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
	clk := newTestClock()
	m.sleep, m.now = clk.sleepCtx, clk.now
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
	if len(none) != 0 || slices.Contains(helloFeatures(off.o, false, false), proto.FeatureUnlock) {
		t.Fatalf("a device without UnlockOS offers unlock: %v", none)
	}
}

func TestLoopbackUnlockOverTheControlChannel(t *testing.T) {
	h := newHarness(t, platform.ConsentRemember, func(o *Options) { o.UnlockOS = "darwin"; o.Keymap = keymap.Mac })
	clk := newTestClock()
	h.mgr.sleep, h.mgr.now = clk.sleepCtx, clk.now
	h.backend.SetLocked(true)
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	hello := h.waitControl(t, "hello")
	if !slices.Contains(hello["features"].([]any), any(proto.FeatureUnlock)) || hello["locked"] != true {
		t.Fatalf("hello features=%v locked=%v", hello["features"], hello["locked"])
	}
	shift := keyEvents(keymap.Mac, usageLShift)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(h.backend.Injected(), shift) {
		if time.Now().After(deadline) {
			t.Fatalf("opening a session on the lock screen sent %+v, want Shift to raise the password field", h.backend.Injected())
		}
		time.Sleep(10 * time.Millisecond)
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
	km := keymap.Mac
	if want := slices.Concat(shift, keyEvents(km, usageLGUI, usageA), keyEvents(km, usageEnter)); !slices.Equal(h.backend.Injected(), want) {
		t.Fatalf("keys %+v, want the Shift from the session opening reused, not sent again", h.backend.Injected())
	}
	if m := send(2); m["ok"] != false || m["code"] != unlockTooSoon {
		t.Fatalf("a second attempt right away = %v, want too_soon", m)
	}
}
