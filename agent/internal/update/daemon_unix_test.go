//go:build unix

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
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

type fakeRunner struct {
	calls      []string
	version    string // CFBundleShortVersionString of the app
	newVersion string // after installer
	installErr error
	onInstall  func() // runs inside installer, like the pkg's postinstall
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	switch name {
	case "plutil":
		return []byte(r.version + "\n"), nil
	case "installer":
		if r.installErr != nil {
			return []byte("installer: The install failed."), r.installErr
		}
		r.version = r.newVersion
		if r.onInstall != nil {
			r.onInstall()
		}
		return []byte("installer: The upgrade was successful."), nil
	}
	return nil, nil
}

type daemonFixture struct {
	t     *testing.T
	d     *Daemon
	run   *fakeRunner
	priv  ed25519.PrivateKey
	home  string
	tmp   string
	up    bool
	user  string
	runAt time.Time
}

func newDaemonFixture(t *testing.T) *daemonFixture {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tmp := t.TempDir()
	f := &daemonFixture{t: t, run: &fakeRunner{version: "0.3.0", newVersion: "0.3.1"}, priv: priv, tmp: tmp,
		home: filepath.Join(tmp, "Users", "ann"), up: true, user: "ann", runAt: time.Now()}
	for _, d := range []string{filepath.Join(f.home, "Library", "Application Support", "RemoteAgent", "updates"),
		filepath.Join(tmp, "Applications", "RemoteAgent.app"), filepath.Join(tmp, "LaunchAgents"), filepath.Join(tmp, "LaunchDaemons")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.d = &Daemon{
		Root:        filepath.Join(tmp, "Updater"),
		App:         filepath.Join(tmp, "Applications", "RemoteAgent.app"),
		AgentPlist:  filepath.Join(tmp, "LaunchAgents", "com.remotectl.agent.plist"),
		DaemonPlist: filepath.Join(tmp, "LaunchDaemons", "com.remotectl.updater.plist"),
		PubKey:      base64.StdEncoding.EncodeToString(pub),
		Runner:      f.run,
		Console:     func() (string, uint32, bool) { return f.user, 501, f.user != "" },
		UserHome:    func(uint32) string { return f.home },
		ServerUp:    func(context.Context) bool { return f.up },
		Now:         func() time.Time { return f.runAt },
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

func (f *daemonFixture) payloadPath(version string) string {
	return filepath.Join(f.home, "Library", "Application Support", "RemoteAgent", "updates", "remote-agent-"+version+".pkg")
}

// request writes a signed install request for payload (as written to the
// user's updates dir); mutate may tamper with it after signing.
func (f *daemonFixture) request(id, version string, payload []byte, mutate func(*Request)) {
	if err := os.WriteFile(f.payloadPath(version), payload, 0o600); err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	m := ReqManifest{Version: version, OS: "darwin", Arch: "universal", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, proto.ReleaseMessage(m.Version, m.OS, m.Arch, m.SHA256)))
	r := Request{V: 1, Op: OpInstall, ID: id, Manifest: &m, Payload: f.payloadPath(version)}
	if mutate != nil {
		mutate(&r)
	}
	f.writeRequest(id, r)
}

func (f *daemonFixture) writeRequest(id string, r Request) {
	if err := os.MkdirAll(f.inbox(), 0o755); err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(f.inbox(), id+".json"), b, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *daemonFixture) inbox() string { return filepath.Join(f.d.Root, "inbox") }

func (f *daemonFixture) runDaemon() {
	f.t.Helper()
	if err := f.d.Run(context.Background()); err != nil {
		f.t.Fatalf("daemon: %v", err)
	}
}

func (f *daemonFixture) status() *DaemonStatus {
	b, err := os.ReadFile(filepath.Join(f.d.Root, "status.json"))
	if err != nil {
		return nil
	}
	var s DaemonStatus
	if err := json.Unmarshal(b, &s); err != nil {
		f.t.Fatal(err)
	}
	s.At = ""
	return &s
}

func (f *daemonFixture) marker() *Marker {
	m, err := ReadMarker(filepath.Join(f.d.Root, "marker.json"))
	if err != nil {
		return nil
	}
	return &m
}

func ls(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func failed(id, from, to, msg string) *DaemonStatus {
	return &DaemonStatus{V: 1, ID: id, State: DaemonFailed, From: from, To: to, Error: msg}
}

func TestDaemonInstall(t *testing.T) {
	f := newDaemonFixture(t)
	f.request("r1", "0.3.1", []byte("signed pkg"), nil)
	f.runDaemon()

	app, root := f.d.App, f.d.Root
	plutil := "plutil -extract CFBundleShortVersionString raw " + app + "/Contents/Info.plist"
	wantCalls := []string{plutil,
		"ditto " + app + " " + root + "/previous/RemoteAgent.app",
		"ditto " + f.d.AgentPlist + " " + root + "/previous/com.remotectl.agent.plist",
		"ditto " + f.d.DaemonPlist + " " + root + "/previous/com.remotectl.updater.plist",
		"installer -pkg " + root + "/pkgs/0.3.1.pkg -target /",
		plutil, // installed?
		plutil, // health watch
	}
	if !slices.Equal(f.run.calls, wantCalls) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.run.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if got, want := f.status(), (&DaemonStatus{V: 1, ID: "r1", State: DaemonInstalled, From: "0.3.0", To: "0.3.1"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
	m := f.marker()
	if m == nil || m.ID != "r1" || m.From != "0.3.0" || m.To != "0.3.1" || m.Previous != root+"/previous" {
		t.Fatalf("marker = %+v", m)
	}
	if got := ls(t, f.inbox()); got != nil {
		t.Fatalf("inbox left %q", got)
	}
	if got := ls(t, filepath.Join(root, "pkgs")); got != nil {
		t.Fatalf("pkgs left %q", got)
	}
	log, _ := os.ReadFile(filepath.Join(root, "logs", "install-0.3.1.log"))
	if !strings.Contains(string(log), "The upgrade was successful.") {
		t.Fatalf("install log = %q", log)
	}
}

func TestDaemonLayout(t *testing.T) {
	f := newDaemonFixture(t)
	if err := os.MkdirAll(filepath.Join(f.d.Root, "pkgs"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.d.Root, "pkgs"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.tmp, filepath.Join(f.d.Root, "logs")); err != nil {
		t.Fatal(err)
	}
	f.runDaemon()
	want := map[string]fs.FileMode{".": fs.ModeDir | 0o755, "inbox": fs.ModeDir | fs.ModeSticky | 0o733,
		"pkgs": fs.ModeDir | 0o700, "previous": fs.ModeDir | 0o700, "logs": fs.ModeDir | 0o700}
	got := map[string]fs.FileMode{}
	for name := range want {
		fi, err := os.Lstat(filepath.Join(f.d.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		got[name] = fi.Mode()
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modes = %v, want %v", got, want)
	}
}

func TestDaemonRefusesRequests(t *testing.T) {
	plutil := func(f *daemonFixture) string {
		return "plutil -extract CFBundleShortVersionString raw " + f.d.App + "/Contents/Info.plist"
	}
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	cases := []struct {
		name   string
		setup  func(f *daemonFixture)
		status func(f *daemonFixture) *DaemonStatus
		calls  func(f *daemonFixture) []string
	}{
		{"symlinked request", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), nil)
			real := filepath.Join(f.tmp, "real.json")
			_ = os.Rename(filepath.Join(f.inbox(), "r1.json"), real)
			_ = os.Symlink(real, filepath.Join(f.inbox(), "r1.json"))
		}, func(*daemonFixture) *DaemonStatus { return nil }, func(*daemonFixture) []string { return nil }},
		{"oversize request", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), func(r *Request) { r.Version = strings.Repeat("x", MaxRequestBytes) })
		}, func(*daemonFixture) *DaemonStatus { return nil }, func(*daemonFixture) []string { return nil }},
		{"FIFO request", func(f *daemonFixture) {
			_ = os.MkdirAll(f.inbox(), 0o755)
			if err := syscall.Mkfifo(filepath.Join(f.inbox(), "r1.json"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, func(*daemonFixture) *DaemonStatus { return nil }, func(*daemonFixture) []string { return nil }},
		{"not newer", func(f *daemonFixture) {
			f.run.version = "0.3.1"
			f.request("r1", "0.3.1", []byte("pkg"), nil)
		}, func(*daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.1", "0.3.1", "0.3.1 is not newer than the installed 0.3.1")
		}, func(f *daemonFixture) []string { return []string{plutil(f)} }},
		{"wrong key", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), func(r *Request) {
				m := r.Manifest
				m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(otherKey, proto.ReleaseMessage(m.Version, m.OS, m.Arch, m.SHA256)))
			})
		}, func(*daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.0", "0.3.1", "release signature does not verify")
		}, func(f *daemonFixture) []string { return []string{plutil(f)} }},
		{"payload outside the user's updates dir", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), func(r *Request) { r.Payload = "/etc/remote-agent-0.3.1.pkg" })
		}, func(f *daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.0", "0.3.1", "payload must be "+f.payloadPath("0.3.1"))
		}, func(f *daemonFixture) []string { return []string{plutil(f)} }},
		{"payload symlink", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), nil)
			target := filepath.Join(f.tmp, "elsewhere.pkg")
			_ = os.Rename(f.payloadPath("0.3.1"), target)
			_ = os.Symlink(target, f.payloadPath("0.3.1"))
		}, func(f *daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.0", "0.3.1", "payload: open "+f.payloadPath("0.3.1")+": too many levels of symbolic links")
		}, func(f *daemonFixture) []string { return []string{plutil(f)} }},
		{"payload changed after signing", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), nil)
			_ = os.WriteFile(f.payloadPath("0.3.1"), []byte("pkh"), 0o600) // same size, other bytes
		}, func(*daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.0", "0.3.1", "payload: sha256 mismatch")
		}, func(f *daemonFixture) []string { return []string{plutil(f)} }},
		{"payload larger than signed", func(f *daemonFixture) {
			f.request("r1", "0.3.1", []byte("pkg"), nil)
			_ = os.WriteFile(f.payloadPath("0.3.1"), []byte("pkg and more"), 0o600)
		}, func(*daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.0", "0.3.1", "payload: larger than the signed size 3")
		}, func(f *daemonFixture) []string { return []string{plutil(f)} }},
		{"installer fails", func(f *daemonFixture) {
			f.run.installErr = errors.New("exit status 1")
			f.request("r1", "0.3.1", []byte("pkg"), nil)
		}, func(*daemonFixture) *DaemonStatus {
			return failed("r1", "0.3.0", "0.3.1", "installer: exit status 1 (still 0.3.0)")
		}, func(f *daemonFixture) []string {
			root := f.d.Root
			return []string{plutil(f), "ditto " + f.d.App + " " + root + "/previous/RemoteAgent.app",
				"ditto " + f.d.AgentPlist + " " + root + "/previous/com.remotectl.agent.plist",
				"ditto " + f.d.DaemonPlist + " " + root + "/previous/com.remotectl.updater.plist",
				"installer -pkg " + root + "/pkgs/0.3.1.pkg -target /", plutil(f),
				"launchctl bootstrap gui/501 " + f.d.AgentPlist}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDaemonFixture(t)
			c.setup(f)
			done := make(chan struct{})
			go func() { f.runDaemon(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("daemon hung")
			}
			if got, want := f.status(), c.status(f); !reflect.DeepEqual(got, want) {
				t.Errorf("status = %+v, want %+v", got, want)
			}
			if got, want := f.run.calls, c.calls(f); !slices.Equal(got, want) {
				t.Errorf("calls = %q, want %q", got, want)
			}
			if got := ls(t, f.inbox()); got != nil {
				t.Errorf("inbox left %q", got)
			}
			if got := ls(t, filepath.Join(f.d.Root, "pkgs")); got != nil {
				t.Errorf("pkgs left %q", got)
			}
			if m := f.marker(); m != nil {
				t.Errorf("marker left %+v", m)
			}
		})
	}
}

func TestDaemonHealthy(t *testing.T) {
	cases := []struct {
		name       string
		installed  string
		reqVersion string
		wantMarker bool
		wantState  string
	}{
		{"clears the marker for the installed version", "0.3.1", "0.3.1", false, DaemonHealthy},
		{"ignores another version", "0.3.1", "0.3.2", true, DaemonInstalled},
		{"ignores an older version", "0.3.1", "0.3.0", true, DaemonInstalled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDaemonFixture(t)
			f.run.version = c.installed
			_ = os.MkdirAll(f.d.Root, 0o755)
			now := f.runAt
			_ = WriteMarker(filepath.Join(f.d.Root, "marker.json"), Marker{ID: "r1", From: "0.3.0", To: "0.3.1", AppliedAt: now, WatchFrom: now})
			_ = writeAtomic(filepath.Join(f.d.Root, "status.json"), []byte(`{"v":1,"id":"r1","state":"installed","from":"0.3.0","to":"0.3.1"}`), 0o644)
			f.writeRequest("h1", Request{V: 1, Op: OpHealthy, ID: "h1", Version: c.reqVersion})
			f.runDaemon()
			if got := f.marker() != nil; got != c.wantMarker {
				t.Fatalf("marker present = %v, want %v", got, c.wantMarker)
			}
			if got := f.status(); got == nil || got.State != c.wantState || got.ID != "r1" {
				t.Fatalf("status = %+v, want state %s for r1", got, c.wantState)
			}
		})
	}
}

func TestDaemonWatchdog(t *testing.T) {
	root := func(f *daemonFixture) string { return f.d.Root }
	plutil := func(f *daemonFixture) string {
		return "plutil -extract CFBundleShortVersionString raw " + f.d.App + "/Contents/Info.plist"
	}
	cases := []struct {
		name       string
		installed  string
		applied    time.Duration // before now
		up         bool
		user       string
		wantCalls  func(f *daemonFixture) []string
		wantStatus *DaemonStatus
		wantMarker bool
		watchReset bool
	}{
		{"rolls back a silent build", "0.3.1", 10 * time.Minute, true, "ann", func(f *daemonFixture) []string {
			return []string{plutil(f), "launchctl bootout gui/501/com.remotectl.agent",
				"ditto " + root(f) + "/previous/RemoteAgent.app " + f.d.App,
				"ditto " + root(f) + "/previous/com.remotectl.agent.plist " + f.d.AgentPlist,
				"ditto " + root(f) + "/previous/com.remotectl.updater.plist " + f.d.DaemonPlist,
				"launchctl bootstrap gui/501 " + f.d.AgentPlist}
		}, &DaemonStatus{V: 1, ID: "r1", State: DaemonRolledBack, From: "0.3.0", To: "0.3.1",
			Error: "0.3.1 did not reach the server within 5 minutes"}, false, false},
		{"waits inside the window", "0.3.1", 2 * time.Minute, true, "ann", func(f *daemonFixture) []string { return []string{plutil(f)} },
			nil, true, false},
		{"server down restarts the window", "0.3.1", 10 * time.Minute, false, "ann",
			func(f *daemonFixture) []string { return []string{plutil(f)} }, nil, true, true},
		{"nobody logged in restarts the window", "0.3.1", 10 * time.Minute, true, "loginwindow",
			func(f *daemonFixture) []string { return []string{plutil(f)} }, nil, true, true},
		{"install that never took", "0.3.0", 10 * time.Minute, true, "ann", func(f *daemonFixture) []string { return []string{plutil(f)} },
			&DaemonStatus{V: 1, ID: "r1", State: DaemonFailed, From: "0.3.0", To: "0.3.1", Error: "still on 0.3.0 after installing 0.3.1"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDaemonFixture(t)
			f.run.version, f.up, f.user = c.installed, c.up, c.user
			for _, p := range []string{"previous/RemoteAgent.app/Contents", "inbox"} {
				_ = os.MkdirAll(filepath.Join(f.d.Root, p), 0o755)
			}
			applied := f.runAt.Add(-c.applied)
			_ = WriteMarker(filepath.Join(f.d.Root, "marker.json"), Marker{ID: "r1", From: "0.3.0", To: "0.3.1",
				Previous: filepath.Join(f.d.Root, "previous"), AppliedAt: applied, WatchFrom: applied})
			f.runDaemon()
			if got, want := f.run.calls, c.wantCalls(f); !slices.Equal(got, want) {
				t.Errorf("calls = %q\nwant %q", got, want)
			}
			if got := f.status(); !reflect.DeepEqual(got, c.wantStatus) {
				t.Errorf("status = %+v, want %+v", got, c.wantStatus)
			}
			m := f.marker()
			if (m != nil) != c.wantMarker {
				t.Fatalf("marker = %+v, want present %v", m, c.wantMarker)
			}
			if m != nil && m.WatchFrom.Equal(f.runAt) != c.watchReset {
				t.Errorf("watch_from = %v, reset wanted %v", m.WatchFrom, c.watchReset)
			}
		})
	}
}

func TestDaemonDropsStaleTemporaries(t *testing.T) {
	f := newDaemonFixture(t)
	_ = os.MkdirAll(f.inbox(), 0o755)
	stale, fresh := filepath.Join(f.inbox(), ".tmp-old"), filepath.Join(f.inbox(), ".tmp-new")
	_ = os.WriteFile(stale, []byte("{"), 0o644)
	_ = os.WriteFile(fresh, []byte("{"), 0o644)
	_ = os.Chtimes(stale, f.runAt.Add(-2*time.Minute), f.runAt.Add(-2*time.Minute))
	_ = os.Chtimes(fresh, f.runAt, f.runAt)
	_ = os.Mkdir(filepath.Join(f.inbox(), "junk"), 0o755)
	f.runDaemon()
	if got := ls(t, f.inbox()); !slices.Equal(got, []string{".tmp-new"}) {
		t.Fatalf("inbox = %q, want only the fresh temporary", got)
	}
}

func TestDaemonHealthyDuringInstall(t *testing.T) {
	f := newDaemonFixture(t)
	f.request("r1", "0.3.1", []byte("signed pkg"), nil)
	f.run.onInstall = func() { f.writeRequest("h1", Request{V: 1, Op: OpHealthy, ID: "h1", Version: "0.3.1"}) }
	f.runDaemon()
	if got, want := f.status(), (&DaemonStatus{V: 1, ID: "r1", State: DaemonHealthy, From: "0.3.0", To: "0.3.1"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
	if m := f.marker(); m != nil || ls(t, f.inbox()) != nil {
		t.Fatalf("marker = %+v inbox = %q", m, ls(t, f.inbox()))
	}
}

func TestDaemonRestartsOnlyForTheConsoleUser(t *testing.T) {
	me := uint32(os.Getuid())
	cases := []struct {
		name    string
		user    string
		uid     uint32
		restart bool
	}{
		{"the console user", "ann", me, true},
		{"another user", "ann", me + 1, false},
		{"nobody logged in", "loginwindow", me, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDaemonFixture(t)
			f.d.Console = func() (string, uint32, bool) { return c.user, c.uid, true }
			f.writeRequest("r1", Request{V: 1, Op: OpRestart, ID: "r1"})
			f.runDaemon()
			got := slices.Contains(f.run.calls, "/sbin/shutdown -r now")
			if got != c.restart {
				t.Fatalf("restarted = %v, want %v (calls %v)", got, c.restart, f.run.calls)
			}
		})
	}
}
