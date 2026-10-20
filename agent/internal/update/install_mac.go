package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

// macInstaller is the user agent's side of the root update daemon: it drops
// requests into the daemon's inbox and follows status.json. Only the daemon
// can replace /Applications/RemoteAgent.app.
type macInstaller struct {
	updaterDir   string
	dataDir      string
	version      string
	log          *slog.Logger
	poll         time.Duration
	answerWithin time.Duration // the daemon must take the request by then
	finishWithin time.Duration
}

func (mi *macInstaller) logger() *slog.Logger {
	if mi.log == nil {
		return slog.Default()
	}
	return mi.log
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// drop writes the request as .tmp-<id> and renames it into place, so the
// daemon never sees a partial file.
func (mi *macInstaller) drop(req Request) error {
	inbox := filepath.Join(mi.updaterDir, "inbox")
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tmp := filepath.Join(inbox, ".tmp-"+req.ID)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(inbox, req.ID+".json"))
}

func (mi *macInstaller) readStatus() (DaemonStatus, error) {
	var s DaemonStatus
	b, err := os.ReadFile(filepath.Join(mi.updaterDir, "status.json"))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// Install hands the downloaded pkg to the daemon and waits for its verdict.
// The pkg's preinstall usually stops this agent before the verdict arrives.
func (mi *macInstaller) Install(ctx context.Context, payload string, m api.Manifest) error {
	req := Request{V: 1, Op: OpInstall, ID: newRequestID(), Payload: payload, Manifest: &ReqManifest{
		Version: m.Version, OS: m.OS, Arch: m.Arch, SHA256: m.SHA256, Size: m.Size, Signature: m.Signature}}
	if err := mi.drop(req); err != nil {
		return errors.New("the update daemon's inbox is not writable; reinstall the agent")
	}
	start := time.Now()
	seen := false
	tick := time.NewTicker(mi.poll)
	defer tick.Stop()
	for {
		if st, err := mi.readStatus(); err == nil && st.ID == req.ID {
			seen = true
			switch st.State {
			case DaemonInstalled, DaemonHealthy:
				return nil
			case DaemonFailed, DaemonRolledBack:
				return errors.New(st.Error)
			}
		}
		switch {
		case !seen && time.Since(start) > mi.answerWithin:
			_ = os.Remove(filepath.Join(mi.updaterDir, "inbox", req.ID+".json"))
			return errors.New("the update daemon did not pick up the request; reinstall the agent")
		case time.Since(start) > mi.finishWithin:
			return errors.New("the update daemon did not finish the install")
		}
		select {
		case <-ctx.Done():
			return nil // stopped for the install
		case <-tick.C:
		}
	}
}

func (mi *macInstaller) reportedPath() string {
	return filepath.Join(mi.dataDir, "updates", "reported-id")
}

func (mi *macInstaller) reported() string {
	b, _ := os.ReadFile(mi.reportedPath())
	return strings.TrimSpace(string(b))
}

func (mi *macInstaller) saveReported(id string) {
	_ = os.MkdirAll(filepath.Dir(mi.reportedPath()), 0o700)
	_ = os.WriteFile(mi.reportedPath(), []byte(id+"\n"), 0o600)
}

// Watch acts on the daemon's last status at startup: a freshly installed
// build tells the daemon it is healthy once it reached the server; a failed
// or rolled-back install is reported once and skip-listed.
func (mi *macInstaller) Watch(ctx context.Context, acked <-chan struct{}, report func(proto.UpdateStatus)) {
	st, err := mi.readStatus()
	if err != nil {
		return
	}
	switch startupAction(st, mi.version, mi.reported()) {
	case startupHealthy:
		if !waitClosed(ctx, acked) {
			return
		}
		if err := mi.drop(Request{V: 1, Op: OpHealthy, ID: newRequestID(), Version: mi.version}); err != nil {
			mi.logger().Warn("update: healthy request", "err", err)
		}
	case startupReport:
		_ = AddSkip(mi.dataDir, st.To) // before hello_ack can start another check
		if !waitClosed(ctx, acked) {
			return
		}
		state := proto.UpdateFailed
		if st.State == DaemonRolledBack {
			state = proto.UpdateRolledBack
		}
		report(proto.UpdateStatus{State: state, From: st.From, Version: st.To, Error: st.Error})
		mi.saveReported(st.ID)
	}
}
