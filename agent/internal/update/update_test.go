package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

func TestEligible(t *testing.T) {
	m := api.Manifest{Version: "0.3.1", OS: "darwin", Arch: "universal", SHA256: "ab", Size: 10}
	with := func(f func(*api.Manifest)) api.Manifest { c := m; f(&c); return c }
	skip := map[string]bool{"0.3.1": true}
	cases := []struct {
		name     string
		m        api.Manifest
		running  string
		goos     string
		goarch   string
		skip     map[string]bool
		explicit bool
		want     string // "" = eligible
	}{
		{"universal on arm64", m, "0.3.0", "darwin", "arm64", nil, false, ""},
		{"exact arch", with(func(m *api.Manifest) { m.Arch = "amd64" }), "0.3.0", "darwin", "amd64", nil, false, ""},
		{"dev build takes any release", m, "dev", "darwin", "arm64", nil, false, ""},
		{"prerelease to release", m, "0.3.1-dev.4", "darwin", "arm64", nil, false, ""},
		{"same version", m, "0.3.1", "darwin", "arm64", nil, false, "0.3.1 is not newer than 0.3.1"},
		{"downgrade", m, "0.10.0", "darwin", "arm64", nil, false, "0.3.1 is not newer than 0.10.0"},
		{"other os", m, "0.3.0", "windows", "amd64", nil, false, "release is for darwin/universal, this device is windows/amd64"},
		{"other arch", with(func(m *api.Manifest) { m.Arch = "amd64" }), "0.3.0", "darwin", "arm64", nil, false,
			"release is for darwin/amd64, this device is darwin/arm64"},
		{"invalid version", with(func(m *api.Manifest) { m.Version = "latest" }), "0.3.0", "darwin", "arm64", nil, false,
			`release version "latest" is not semantic`},
		{"no size", with(func(m *api.Manifest) { m.Size = 0 }), "0.3.0", "darwin", "arm64", nil, false, "release size 0 out of range"},
		{"too large", with(func(m *api.Manifest) { m.Size = MaxPayload + 1 }), "0.3.0", "darwin", "arm64", nil, false,
			"release size 536870913 out of range"},
		{"skip-listed", m, "0.3.0", "darwin", "arm64", skip, false, "0.3.1 failed on this device before; skipped"},
		{"explicit ignores the skip-list", m, "0.3.0", "darwin", "arm64", skip, true, ""},
	}
	for _, c := range cases {
		err := eligible(c.m, c.running, c.goos, c.goarch, c.skip, c.explicit)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: eligible = %q, want %q", c.name, got, c.want)
		}
	}
}

// ---- CheckNow against a fake server ----

const goos, goarch = "windows", "amd64"

type release struct {
	version string
	payload []byte // served bytes
	m       api.Manifest
}

type fixture struct {
	t        *testing.T
	priv     ed25519.PrivateKey
	dataDir  string
	srv      *httptest.Server
	checks   atomic.Int32
	explicit []string

	mu       sync.Mutex
	rel      *release
	statuses []proto.UpdateStatus
	changed  chan struct{}
	installs []string // payload contents handed to the installer
	ended    []string

	installErr error
	busy       atomic.Bool
	endIdles   bool // EndAll makes the device idle
}

func newFixture(t *testing.T) *fixture {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fixture{t: t, priv: priv, dataDir: t.TempDir(), changed: make(chan struct{}, 100)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/agent/update", func(w http.ResponseWriter, r *http.Request) {
		f.checks.Add(1)
		f.mu.Lock()
		f.explicit = append(f.explicit, r.URL.Query().Get("explicit"))
		rel := f.rel
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("os") != goos {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if rel == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(rel.m)
	})
	mux.HandleFunc("GET /v1/releases/{v}/file", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		rel := f.rel
		f.mu.Unlock()
		if rel == nil || r.PathValue("v") != rel.version {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(rel.payload)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// offer publishes version with payload; signed/hashed as given (nil = honest).
func (f *fixture) offer(version string, payload []byte, mutate func(*api.Manifest)) {
	sum := sha256.Sum256(payload)
	m := api.Manifest{Version: version, OS: goos, Arch: goarch, URL: "/v1/releases/" + version + "/file",
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}
	if mutate != nil {
		mutate(&m)
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, proto.ReleaseMessage(m.Version, m.OS, m.Arch, m.SHA256)))
	f.mu.Lock()
	f.rel = &release{version: version, payload: payload, m: m}
	f.mu.Unlock()
}

func (f *fixture) Install(_ context.Context, payload string, m api.Manifest) error {
	b, err := os.ReadFile(payload)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs = append(f.installs, m.Version+":"+string(b))
	return f.installErr
}

func (f *fixture) updater(pub string) *Updater {
	if pub == "" {
		pub = base64.StdEncoding.EncodeToString(f.priv.Public().(ed25519.PublicKey))
	}
	return New(Options{
		API: api.New(f.srv.URL), Token: func(context.Context) (string, error) { return "tok", nil },
		Version: "0.3.0", ReleasePubKey: pub, DataDir: f.dataDir, GOOS: goos, GOARCH: goarch,
		Busy: f.busy.Load,
		EndAll: func(reason string) {
			f.mu.Lock()
			f.ended = append(f.ended, reason)
			f.mu.Unlock()
			if f.endIdles {
				f.busy.Store(false)
			}
		},
		OnStatus: func(s proto.UpdateStatus) {
			f.mu.Lock()
			f.statuses = append(f.statuses, s)
			f.mu.Unlock()
			f.changed <- struct{}{}
		},
		Installer: f, IdlePoll: time.Millisecond, StatusEvery: -1,
	})
}

func (f *fixture) waitStatus(state, version string) {
	f.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.statuses)
		last := proto.UpdateStatus{}
		if n > 0 {
			last = f.statuses[n-1]
		}
		f.mu.Unlock()
		if last.State == state && last.Version == version {
			return
		}
		select {
		case <-f.changed:
		case <-deadline:
			f.t.Fatalf("no %s %s status; have %+v", state, version, f.statuses)
		}
	}
}

func st(state, version, err string) proto.UpdateStatus {
	return proto.UpdateStatus{State: state, From: "0.3.0", Version: version, Error: err}
}

func updatesDir(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dataDir, "updates"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCheckNow(t *testing.T) {
	good := []byte("new agent build")
	other := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cases := []struct {
		name      string
		setup     func(f *fixture)
		trigger   Trigger
		pub       string
		want      []proto.UpdateStatus
		installs  []string
		skipped   map[string]bool
		files     []string
		ended     []string
		explicitQ string
	}{
		{name: "periodic, nothing new", trigger: Trigger{}, want: nil, files: nil, skipped: map[string]bool{}, explicitQ: "0"},
		{name: "explicit, nothing new", trigger: Trigger{Explicit: true},
			want: []proto.UpdateStatus{st("checking", "", ""), st("up_to_date", "", "")}, skipped: map[string]bool{}, explicitQ: "1"},
		{name: "periodic install", setup: func(f *fixture) { f.offer("0.3.1", good, nil) },
			want:     []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("installing", "0.3.1", "")},
			installs: []string{"0.3.1:new agent build"}, skipped: map[string]bool{}, files: []string{"remote-agent-0.3.1.msi"}, explicitQ: "0"},
		{name: "explicit install", setup: func(f *fixture) { f.offer("0.3.1", good, nil) }, trigger: Trigger{Explicit: true},
			want:     []proto.UpdateStatus{st("checking", "", ""), st("downloading", "0.3.1", ""), st("installing", "0.3.1", "")},
			installs: []string{"0.3.1:new agent build"}, skipped: map[string]bool{}, files: []string{"remote-agent-0.3.1.msi"}, explicitQ: "1"},
		{name: "sha mismatch", setup: func(f *fixture) {
			f.offer("0.3.1", good, func(m *api.Manifest) { m.SHA256 = hex.EncodeToString(make([]byte, 32)) })
		}, want: []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("failed", "0.3.1", "download: sha256 mismatch")},
			skipped: map[string]bool{"0.3.1": true}, files: []string{"skip.json"}, explicitQ: "0"},
		{name: "payload larger than its size", setup: func(f *fixture) {
			f.offer("0.3.1", good, func(m *api.Manifest) { m.Size = 4 })
		}, want: []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("failed", "0.3.1", "download: larger than the signed size 4")},
			skipped: map[string]bool{"0.3.1": true}, files: []string{"skip.json"}, explicitQ: "0"},
		{name: "payload shorter than its size", setup: func(f *fixture) {
			f.offer("0.3.1", good, func(m *api.Manifest) { m.Size = 100 })
		}, want: []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("failed", "0.3.1", "download: got 15 of 100 bytes")},
			skipped: map[string]bool{}, files: nil, explicitQ: "0"},
		{name: "signed with another key", setup: func(f *fixture) { f.offer("0.3.1", good, nil) }, pub: other, trigger: Trigger{Explicit: true},
			want:    []proto.UpdateStatus{st("checking", "", ""), st("failed", "0.3.1", "release signature does not verify")},
			skipped: map[string]bool{"0.3.1": true}, files: []string{"skip.json"}, explicitQ: "1"},
		{name: "not newer", setup: func(f *fixture) { f.offer("0.3.0", good, nil) }, trigger: Trigger{Explicit: true},
			want:    []proto.UpdateStatus{st("checking", "", ""), st("failed", "0.3.0", "0.3.0 is not newer than 0.3.0")},
			skipped: map[string]bool{}, explicitQ: "1"},
		{name: "skip-listed version is not retried automatically", setup: func(f *fixture) {
			f.offer("0.3.1", good, nil)
			_ = AddSkip(f.dataDir, "0.3.1")
		}, want: nil, skipped: map[string]bool{"0.3.1": true}, files: []string{"skip.json"}, explicitQ: "0"},
		{name: "explicit retries a skip-listed version", setup: func(f *fixture) {
			f.offer("0.3.1", good, nil)
			_ = AddSkip(f.dataDir, "0.3.1")
		}, trigger: Trigger{Explicit: true},
			want:     []proto.UpdateStatus{st("checking", "", ""), st("downloading", "0.3.1", ""), st("installing", "0.3.1", "")},
			installs: []string{"0.3.1:new agent build"}, skipped: map[string]bool{"0.3.1": true},
			files: []string{"remote-agent-0.3.1.msi", "skip.json"}, explicitQ: "1"},
		{name: "installer fails", setup: func(f *fixture) {
			f.offer("0.3.1", good, nil)
			f.installErr = errors.New("installer exited with 1603")
		}, want: []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("installing", "0.3.1", ""),
			st("failed", "0.3.1", "installer exited with 1603")},
			installs: []string{"0.3.1:new agent build"}, skipped: map[string]bool{"0.3.1": true},
			files: []string{"remote-agent-0.3.1.msi", "skip.json"}, explicitQ: "0"},
		{name: "force ends the session instead of waiting", setup: func(f *fixture) {
			f.offer("0.3.1", good, nil)
			f.busy.Store(true)
			f.endIdles = true
		}, trigger: Trigger{Explicit: true, Force: true},
			want:     []proto.UpdateStatus{st("checking", "", ""), st("downloading", "0.3.1", ""), st("installing", "0.3.1", "")},
			installs: []string{"0.3.1:new agent build"}, skipped: map[string]bool{}, files: []string{"remote-agent-0.3.1.msi"},
			ended: []string{proto.EndAgentUpdate}, explicitQ: "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.setup != nil {
				c.setup(f)
			}
			f.updater(c.pub).CheckNow(context.Background(), c.trigger)
			if !reflect.DeepEqual(f.statuses, c.want) {
				t.Errorf("statuses = %+v\nwant %+v", f.statuses, c.want)
			}
			if !reflect.DeepEqual(f.installs, c.installs) || !reflect.DeepEqual(f.ended, c.ended) {
				t.Errorf("installs = %q ended = %q, want %q %q", f.installs, f.ended, c.installs, c.ended)
			}
			if got := LoadSkip(f.dataDir); !reflect.DeepEqual(got, c.skipped) {
				t.Errorf("skip list = %v, want %v", got, c.skipped)
			}
			if got := updatesDir(t, f.dataDir); !slices.Equal(got, c.files) {
				t.Errorf("updates dir = %q, want %q", got, c.files)
			}
			if !slices.Equal(f.explicit, []string{c.explicitQ}) {
				t.Errorf("explicit query = %q, want [%q]", f.explicit, c.explicitQ)
			}
		})
	}
}

func TestCheckNowWaitsForIdle(t *testing.T) {
	f := newFixture(t)
	f.offer("0.3.1", []byte("build"), nil)
	f.busy.Store(true)
	u := f.updater("")
	done := make(chan struct{})
	go func() { u.CheckNow(context.Background(), Trigger{}); close(done) }()
	f.waitStatus("waiting_idle", "0.3.1")
	f.busy.Store(false)
	<-done
	want := []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("waiting_idle", "0.3.1", ""), st("installing", "0.3.1", "")}
	if !reflect.DeepEqual(f.statuses, want) || f.ended != nil {
		t.Fatalf("statuses = %+v ended = %v", f.statuses, f.ended)
	}
}

func TestForceUpgradesTheWait(t *testing.T) {
	f := newFixture(t)
	f.offer("0.3.1", []byte("build"), nil)
	f.busy.Store(true)
	f.endIdles = true
	u := f.updater("")
	done := make(chan struct{})
	go func() { u.CheckNow(context.Background(), Trigger{Explicit: true}); close(done) }()
	f.waitStatus("waiting_idle", "0.3.1")
	u.CheckNow(context.Background(), Trigger{Explicit: true, Force: true}) // coalesced: returns at once
	<-done
	want := []proto.UpdateStatus{st("checking", "", ""), st("downloading", "0.3.1", ""), st("waiting_idle", "0.3.1", ""),
		st("installing", "0.3.1", "")}
	if !reflect.DeepEqual(f.statuses, want) || !slices.Equal(f.ended, []string{proto.EndAgentUpdate}) || f.checks.Load() != 1 {
		t.Fatalf("statuses = %+v ended = %v checks = %d", f.statuses, f.ended, f.checks.Load())
	}
	u.CheckNow(context.Background(), Trigger{Explicit: true})
	if f.checks.Load() != 1 {
		t.Fatal("checked again after handing an install to the OS")
	}
}

func TestNewerTriggerSupersedesTheWait(t *testing.T) {
	f := newFixture(t)
	f.offer("0.3.1", []byte("build 1"), nil)
	f.busy.Store(true)
	u := f.updater("")
	done := make(chan struct{})
	go func() { u.CheckNow(context.Background(), Trigger{}); close(done) }()
	f.waitStatus("waiting_idle", "0.3.1")
	f.offer("0.3.2", []byte("build 2"), nil)
	u.CheckNow(context.Background(), Trigger{})
	f.waitStatus("waiting_idle", "0.3.2")
	f.busy.Store(false)
	<-done
	want := []proto.UpdateStatus{st("downloading", "0.3.1", ""), st("waiting_idle", "0.3.1", ""),
		st("downloading", "0.3.2", ""), st("waiting_idle", "0.3.2", ""), st("installing", "0.3.2", "")}
	if !reflect.DeepEqual(f.statuses, want) || !slices.Equal(f.installs, []string{"0.3.2:build 2"}) {
		t.Fatalf("statuses = %+v installs = %q", f.statuses, f.installs)
	}
}

type pendingInstaller struct{ *fixture }

func (pendingInstaller) Pending() bool { return true }

// While the last install's outcome is unknown (msiexec may still be rolling
// it back), no new attempt may start a second installer.
func TestPendingInstallBlocksAttempts(t *testing.T) {
	for _, c := range []struct {
		name    string
		trigger Trigger
		want    []proto.UpdateStatus
	}{
		{"periodic", Trigger{}, nil},
		{"explicit", Trigger{Explicit: true}, []proto.UpdateStatus{st("failed", "", errPending.Error())}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.offer("0.3.1", []byte("build"), nil)
			u := f.updater("")
			u.o.Installer = pendingInstaller{f}
			u.CheckNow(context.Background(), c.trigger)
			if !reflect.DeepEqual(f.statuses, c.want) || f.installs != nil || f.explicit != nil {
				t.Fatalf("statuses = %+v installs = %q checks = %q, want %+v and no check", f.statuses, f.installs, f.explicit, c.want)
			}
		})
	}
}
