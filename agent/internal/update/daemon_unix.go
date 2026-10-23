//go:build unix

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Runner runs the system tools the daemon drives (plutil, ditto, installer,
// launchctl); faked in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

const staleTemp = 60 * time.Second

// Daemon is the root update daemon (`remote-agent update-daemon`). Each run
// re-asserts its directory layout, handles the inbox and then the health
// watch of a pending install. It trusts nothing a user wrote.
type Daemon struct {
	Root        string // .../RemoteAgent/Updater
	App         string // /Applications/RemoteAgent.app
	AgentPlist  string // the LaunchAgent, restored on rollback
	DaemonPlist string
	PubKey      string
	Runner      Runner
	Console     func() (name string, uid uint32, ok bool)
	UserHome    func(uid uint32) string
	ServerUp    func(ctx context.Context) bool
	Now         func() time.Time
	Log         *slog.Logger
}

func (d *Daemon) path(p ...string) string { return filepath.Join(append([]string{d.Root}, p...)...) }

func (d *Daemon) Run(ctx context.Context) error {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if err := d.ensureLayout(); err != nil {
		return err
	}
	lock, err := os.OpenFile(d.path(".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		d.Log.Info("updater: another run holds the lock")
		return nil
	}
	for range 10 { // requests arriving while one installs
		if !d.processInbox(ctx) {
			break
		}
	}
	d.watch(ctx)
	return nil
}

// ensureLayout fixes owner, type and mode of the daemon's directories.
func (d *Daemon) ensureLayout() error {
	dirs := []struct {
		path string
		mode fs.FileMode
	}{
		{filepath.Dir(d.Root), 0o755}, {d.Root, 0o755}, {d.path("inbox"), fs.ModeSticky | 0o733},
		{d.path("pkgs"), 0o700}, {d.path("previous"), 0o700}, {d.path("logs"), 0o700},
	}
	for _, dir := range dirs {
		fi, err := os.Lstat(dir.path)
		if err == nil && !fi.IsDir() {
			if err := os.Remove(dir.path); err != nil {
				return err
			}
		}
		if err := os.Mkdir(dir.path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if os.Geteuid() == 0 {
			if err := os.Lchown(dir.path, 0, 0); err != nil {
				return err
			}
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return err
		}
	}
	return nil
}

// processInbox handles every request once; it reports whether it found any.
func (d *Daemon) processInbox(ctx context.Context) bool {
	entries, err := os.ReadDir(d.path("inbox"))
	if err != nil {
		d.Log.Error("updater: inbox", "err", err)
		return false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	found := false
	for _, e := range entries {
		name := e.Name()
		full := d.path("inbox", name)
		if strings.HasPrefix(name, ".tmp-") {
			if fi, err := os.Lstat(full); err == nil && fi.Mode().IsRegular() && d.Now().Sub(fi.ModTime()) < staleTemp {
				continue // the agent is still writing it
			}
			_ = os.RemoveAll(full)
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			_ = os.RemoveAll(full)
			continue
		}
		found = true
		b, uid, err := readRequest(d.path("inbox"), name)
		if err != nil {
			d.Log.Warn("updater: request refused", "name", name, "err", err)
			continue
		}
		var req Request
		if err := json.Unmarshal(b, &req); err != nil {
			d.Log.Warn("updater: request is not JSON", "name", name)
			continue
		}
		switch req.Op {
		case OpHealthy:
			d.healthy(ctx, req)
		case OpRestart:
			d.restart(ctx, req, uid)
		default:
			d.install(ctx, req, uid)
		}
	}
	return found
}

// readRequest reads and removes inbox/name: a regular file of at most
// MaxRequestBytes, opened without following links or blocking on FIFOs and
// checked to be the inode that was inspected. It returns the owner's uid.
func readRequest(dir, name string) ([]byte, uint32, error) {
	full := filepath.Join(dir, name)
	defer os.Remove(full)
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("not a regular file (%s)", fi.Mode().Type())
	}
	if fi.Size() > MaxRequestBytes {
		return nil, 0, fmt.Errorf("%d bytes, over %d", fi.Size(), MaxRequestBytes)
	}
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi2, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !fi2.Mode().IsRegular() || !os.SameFile(fi, fi2) {
		return nil, 0, errors.New("file changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxRequestBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(b) > MaxRequestBytes {
		return nil, 0, errors.New("grew while reading")
	}
	return b, fi2.Sys().(*syscall.Stat_t).Uid, nil
}

// copyHashed copies the regular file src (not followed if a link) to the new
// file dst, at exactly size bytes and with SHA-256 wantSHA; dst is removed
// on any mismatch.
func copyHashed(src, dst string, size int64, wantSHA string) (err error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	_ = os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, size+1))
	switch {
	case err != nil:
		return err
	case n > size:
		return fmt.Errorf("larger than the signed size %d", size)
	case n < size:
		return fmt.Errorf("got %d of %d bytes", n, size)
	case hex.EncodeToString(h.Sum(nil)) != strings.ToLower(wantSHA):
		return errors.New("sha256 mismatch")
	}
	return nil
}

func (d *Daemon) writeStatus(s DaemonStatus) {
	s.V = 1
	s.At = d.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(s)
	if err := writeAtomic(d.path("status.json"), b, 0o644); err != nil {
		d.Log.Error("updater: status", "err", err)
	}
}

func (d *Daemon) installedVersion(ctx context.Context) string {
	out, err := d.Runner.Run(ctx, "plutil", "-extract", "CFBundleShortVersionString", "raw",
		filepath.Join(d.App, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (d *Daemon) install(ctx context.Context, req Request, uid uint32) {
	from := d.installedVersion(ctx)
	to := ""
	if req.Manifest != nil {
		to = req.Manifest.Version
	}
	fail := func(err error) {
		d.Log.Error("updater: install refused", "id", req.ID, "to", to, "err", err)
		if reRequestID.MatchString(req.ID) {
			d.writeStatus(DaemonStatus{ID: req.ID, State: DaemonFailed, From: from, To: to, Error: err.Error()})
		}
	}
	if err := ValidateRequest(req, from, d.PubKey, d.UserHome(uid)); err != nil {
		fail(err)
		return
	}
	d.writeStatus(DaemonStatus{ID: req.ID, State: DaemonVerifying, From: from, To: to})
	m := req.Manifest
	pkg := d.path("pkgs", m.Version+".pkg")
	defer os.Remove(pkg)
	if err := copyHashed(req.Payload, pkg, m.Size, m.SHA256); err != nil {
		fail(fmt.Errorf("payload: %w", err))
		return
	}
	if err := d.backup(ctx); err != nil {
		fail(fmt.Errorf("backup: %w", err))
		return
	}
	now := d.Now()
	if err := WriteMarker(d.path("marker.json"), Marker{ID: req.ID, From: from, To: to, Previous: d.path("previous"),
		AppliedAt: now, WatchFrom: now}); err != nil {
		fail(err)
		return
	}
	d.writeStatus(DaemonStatus{ID: req.ID, State: DaemonInstalling, From: from, To: to})
	d.Log.Info("updater: installing", "id", req.ID, "from", from, "to", to)
	out, err := d.Runner.Run(ctx, "installer", "-pkg", pkg, "-target", "/")
	_ = os.WriteFile(d.path("logs", "install-"+m.Version+".log"), out, 0o600)
	if got := d.installedVersion(ctx); got != to {
		_ = os.Remove(d.path("marker.json"))
		// preinstall booted the agent out; bring the old build back.
		if name, uid, ok := d.Console(); ok && name != "root" && name != "loginwindow" {
			_, _ = d.Runner.Run(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), d.AgentPlist)
		}
		if err == nil {
			err = errors.New("finished")
		}
		fail(fmt.Errorf("installer: %v (still %s)", err, got))
		return
	}
	d.writeStatus(DaemonStatus{ID: req.ID, State: DaemonInstalled, From: from, To: to})
}

// backup keeps the installed bundle and both plists in previous/.
func (d *Daemon) backup(ctx context.Context) error {
	prev := d.path("previous")
	entries, _ := os.ReadDir(prev)
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(prev, e.Name())); err != nil {
			return err
		}
	}
	for _, src := range []string{d.App, d.AgentPlist, d.DaemonPlist} {
		if out, err := d.Runner.Run(ctx, "ditto", src, filepath.Join(prev, filepath.Base(src))); err != nil {
			return fmt.Errorf("ditto %s: %v: %s", src, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (d *Daemon) healthy(ctx context.Context, req Request) {
	if err := ValidateRequest(req, "", d.PubKey, ""); err != nil {
		d.Log.Warn("updater: healthy request refused", "err", err)
		return
	}
	m, err := ReadMarker(d.path("marker.json"))
	if err != nil {
		return
	}
	if installed := d.installedVersion(ctx); req.Version != m.To || installed != m.To {
		d.Log.Warn("updater: healthy for a version that is not the pending one", "version", req.Version, "installed", installed, "pending", m.To)
		return
	}
	_ = os.Remove(d.path("marker.json"))
	d.writeStatus(DaemonStatus{ID: m.ID, State: DaemonHealthy, From: m.From, To: m.To})
	d.Log.Info("updater: update healthy", "version", m.To)
}

// restart restarts the Mac for the console user's agent; a request from
// anyone else (the inbox is writable by all) is refused.
func (d *Daemon) restart(ctx context.Context, req Request, uid uint32) {
	if err := ValidateRequest(req, "", d.PubKey, ""); err != nil {
		d.Log.Warn("updater: restart request refused", "err", err)
		return
	}
	name, cuid, ok := d.Console()
	if !ok || name == "root" || name == "loginwindow" || cuid != uid {
		d.Log.Warn("updater: restart request not from the console user", "uid", uid, "console", name)
		return
	}
	d.Log.Info("updater: restarting at the console user's request", "user", name)
	if out, err := d.Runner.Run(ctx, "/sbin/shutdown", "-r", "now"); err != nil {
		d.Log.Error("updater: shutdown -r failed", "err", err, "out", strings.TrimSpace(string(out)))
	}
}

// watch applies DecideHealth to a pending install (the 120 s watchdog).
func (d *Daemon) watch(ctx context.Context) {
	m, err := ReadMarker(d.path("marker.json"))
	if err != nil {
		return
	}
	now := d.Now()
	name, uid, ok := d.Console()
	present := ok && name != "" && name != "root" && name != "loginwindow"
	running := d.installedVersion(ctx)
	up := present && d.ServerUp(ctx)
	if !present || !up {
		m.WatchFrom = now
	}
	start := m.AppliedAt
	if m.WatchFrom.After(start) {
		start = m.WatchFrom
	}
	action := DecideHealth(HealthInput{Marker: m, Running: running, ServerUp: up, UserPresent: present,
		Since: now.Sub(start), Now: now})
	d.Log.Info("updater: health", "action", action, "running", running, "to", m.To)
	switch action {
	case HealthWait:
		_ = WriteMarker(d.path("marker.json"), m)
	case HealthHealthy, HealthAbandon:
		_ = os.Remove(d.path("marker.json"))
	case HealthReportFailed:
		_ = os.Remove(d.path("marker.json"))
		d.writeStatus(DaemonStatus{ID: m.ID, State: DaemonFailed, From: m.From, To: m.To,
			Error: fmt.Sprintf("still on %s after installing %s", m.From, m.To)})
	case HealthReportRolledBack:
		_ = os.Remove(d.path("marker.json"))
		d.writeStatus(DaemonStatus{ID: m.ID, State: DaemonRolledBack, From: m.From, To: m.To, Error: m.Error})
	case HealthRollback:
		d.rollback(ctx, m, uid, present)
	}
}

func (d *Daemon) rollback(ctx context.Context, m Marker, uid uint32, present bool) {
	reason := rollbackReason(m)
	d.Log.Error("updater: rolling back", "from", m.To, "to", m.From, "reason", reason)
	prevApp := filepath.Join(m.Previous, filepath.Base(d.App))
	if fi, err := os.Lstat(prevApp); err != nil || !fi.IsDir() {
		_ = os.Remove(d.path("marker.json"))
		d.writeStatus(DaemonStatus{ID: m.ID, State: DaemonFailed, From: m.From, To: m.To, Error: reason + "; no previous build kept"})
		return
	}
	label := strings.TrimSuffix(filepath.Base(d.AgentPlist), ".plist")
	if present {
		_, _ = d.Runner.Run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, label))
	}
	if err := os.RemoveAll(d.App); err != nil {
		d.Log.Error("updater: rollback: remove app", "err", err)
	}
	var failed []string
	for _, dst := range []string{d.App, d.AgentPlist, d.DaemonPlist} {
		if out, err := d.Runner.Run(ctx, "ditto", filepath.Join(m.Previous, filepath.Base(dst)), dst); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v: %s", dst, err, strings.TrimSpace(string(out))))
		}
	}
	if present {
		_, _ = d.Runner.Run(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), d.AgentPlist)
	}
	_ = os.Remove(d.path("marker.json"))
	if len(failed) > 0 {
		reason += "; restore failed: " + strings.Join(failed, "; ")
	}
	d.writeStatus(DaemonStatus{ID: m.ID, State: DaemonRolledBack, From: m.From, To: m.To, Error: reason})
}
