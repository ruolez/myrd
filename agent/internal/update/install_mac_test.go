package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

func newMacInstaller(t *testing.T) *macInstaller {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Updater", "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &macInstaller{updaterDir: filepath.Join(root, "Updater"), dataDir: filepath.Join(root, "data"), version: "0.3.0",
		poll: time.Millisecond, answerWithin: time.Second, finishWithin: 5 * time.Second}
}

// takeRequest waits for one request in the inbox, as the daemon sees it.
func takeRequest(t *testing.T, mi *macInstaller) (string, Request) {
	t.Helper()
	inbox := filepath.Join(mi.updaterDir, "inbox")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(inbox)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				b, err := os.ReadFile(filepath.Join(inbox, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				var r Request
				if err := json.Unmarshal(b, &r); err != nil {
					t.Fatal(err)
				}
				_ = os.Remove(filepath.Join(inbox, e.Name()))
				return e.Name(), r
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no request in the inbox")
	return "", Request{}
}

func daemonSays(t *testing.T, mi *macInstaller, s DaemonStatus) {
	t.Helper()
	s.V = 1
	b, _ := json.Marshal(s)
	if err := writeAtomic(filepath.Join(mi.updaterDir, "status.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMacInstall(t *testing.T) {
	m := api.Manifest{Version: "0.3.1", OS: "darwin", Arch: "universal", URL: "/v1/releases/9/file", SHA256: "ab", Size: 7, Signature: "sig"}
	cases := []struct {
		name  string
		final string
		err   string
		want  string
	}{
		{"installed", DaemonInstalled, "", ""},
		{"failed", DaemonFailed, "release signature does not verify", "release signature does not verify"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mi := newMacInstaller(t)
			done := make(chan error, 1)
			go func() { done <- mi.Install(context.Background(), "/Users/ann/x/remote-agent-0.3.1.pkg", m) }()
			name, req := takeRequest(t, mi)
			want := Request{V: 1, Op: OpInstall, ID: req.ID, Payload: "/Users/ann/x/remote-agent-0.3.1.pkg",
				Manifest: &ReqManifest{Version: "0.3.1", OS: "darwin", Arch: "universal", SHA256: "ab", Size: 7, Signature: "sig"}}
			if !reflect.DeepEqual(req, want) || name != req.ID+".json" || !reRequestID.MatchString(req.ID) {
				t.Fatalf("request %s = %+v, want %+v", name, req, want)
			}
			daemonSays(t, mi, DaemonStatus{ID: "someone-else", State: DaemonInstalled})
			daemonSays(t, mi, DaemonStatus{ID: req.ID, State: DaemonInstalling})
			time.Sleep(10 * time.Millisecond)
			daemonSays(t, mi, DaemonStatus{ID: req.ID, State: c.final, Error: c.err})
			err := <-done
			if got := errString(err); got != c.want {
				t.Fatalf("Install = %q, want %q", got, c.want)
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestMacInstallWithoutDaemon(t *testing.T) {
	mi := newMacInstaller(t)
	mi.answerWithin = 20 * time.Millisecond
	err := mi.Install(context.Background(), "/p.pkg", api.Manifest{Version: "0.3.1"})
	if got, want := errString(err), "the update daemon did not pick up the request; reinstall the agent"; got != want {
		t.Fatalf("Install = %q, want %q", got, want)
	}
}

func TestMacWatch(t *testing.T) {
	type out struct {
		Reports []proto.UpdateStatus
		Healthy []string // versions of healthy requests
		Skip    map[string]bool
	}
	cases := []struct {
		name     string
		st       DaemonStatus
		reported string
		want     out
	}{
		{"new build reports healthy", DaemonStatus{ID: "r1", State: DaemonInstalled, From: "0.2.9", To: "0.3.0"}, "",
			out{nil, []string{"0.3.0"}, map[string]bool{}}},
		{"failed install reported once", DaemonStatus{ID: "r1", State: DaemonFailed, From: "0.3.0", To: "0.3.1", Error: "installer: exit status 1"}, "",
			out{[]proto.UpdateStatus{{State: proto.UpdateFailed, From: "0.3.0", Version: "0.3.1", Error: "installer: exit status 1"}}, nil,
				map[string]bool{"0.3.1": true}}},
		{"rollback reported", DaemonStatus{ID: "r2", State: DaemonRolledBack, From: "0.3.0", To: "0.3.1", Error: "0.3.1 did not reach the server within 5 minutes"}, "r1",
			out{[]proto.UpdateStatus{{State: proto.UpdateRolledBack, From: "0.3.0", Version: "0.3.1", Error: "0.3.1 did not reach the server within 5 minutes"}}, nil,
				map[string]bool{"0.3.1": true}}},
		{"already reported", DaemonStatus{ID: "r1", State: DaemonFailed, From: "0.3.0", To: "0.3.1"}, "r1", out{nil, nil, map[string]bool{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mi := newMacInstaller(t)
			daemonSays(t, mi, c.st)
			if c.reported != "" {
				mi.saveReported(c.reported)
			}
			acked := make(chan struct{})
			close(acked)
			var got out
			mi.Watch(context.Background(), acked, func(s proto.UpdateStatus) { got.Reports = append(got.Reports, s) })
			entries, _ := os.ReadDir(filepath.Join(mi.updaterDir, "inbox"))
			for range entries {
				_, r := takeRequest(t, mi)
				if r.Op != OpHealthy || !reRequestID.MatchString(r.ID) {
					t.Fatalf("request %+v", r)
				}
				got.Healthy = append(got.Healthy, r.Version)
			}
			got.Skip = LoadSkip(mi.dataDir)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			var again []proto.UpdateStatus
			mi.Watch(context.Background(), acked, func(s proto.UpdateStatus) { again = append(again, s) })
			if again != nil {
				t.Fatalf("reported twice: %+v", again)
			}
		})
	}
}
