//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

const (
	msiWait     = 30 * time.Minute // longest we watch msiexec before leaving it to the next start
	healthEvery = 15 * time.Second
)

// winInstaller runs in the LocalSystem service. msiexec is started detached
// so it survives the service stop it causes; the marker in the data dir
// carries the attempt across the restart.
type winInstaller struct {
	dataDir string
	version string
	log     *slog.Logger
	probe   func(context.Context) bool
}

func newDefaultInstaller(o Options) Installer {
	return &winInstaller{dataDir: o.DataDir, version: o.Version, log: o.Log, probe: o.HealthProbe}
}

func (w *winInstaller) updatesDir() string { return filepath.Join(w.dataDir, "updates") }
func (w *winInstaller) markerPath() string { return filepath.Join(w.dataDir, markerName) }

// Pending reports an install whose outcome Watch has not settled yet.
func (w *winInstaller) Pending() bool {
	_, err := os.Stat(w.markerPath())
	return err == nil
}

// CountServiceStart records one more start of the service for a pending
// update (crash-loop detection); call it once per process, before connecting.
func CountServiceStart(dataDir string) {
	path := filepath.Join(dataDir, markerName)
	m, err := ReadMarker(path)
	if err != nil {
		return
	}
	m.Starts++
	_ = WriteMarker(path, m)
}

func (w *winInstaller) Install(ctx context.Context, payload string, m api.Manifest) error {
	if err := w.checkAuthenticode(payload); err != nil {
		return err
	}
	msi := filepath.Join(w.updatesDir(), msiName(m.Version))
	if err := os.Rename(payload, msi); err != nil {
		return err
	}
	prev := filepath.Join(w.updatesDir(), installedMSI)
	if _, err := os.Stat(prev); err != nil {
		prev = "" // first update after a manual install: nothing to roll back to
	}
	if err := WriteMarker(w.markerPath(), Marker{From: w.version, To: m.Version, Previous: prev, AppliedAt: time.Now()}); err != nil {
		return err
	}
	logPath := filepath.Join(w.updatesDir(), msiLogName(m.Version))
	_ = os.Remove(logPath)
	if err := startMSI(msi, logPath); err != nil {
		_ = os.Remove(w.markerPath())
		return fmt.Errorf("start msiexec: %w", err)
	}
	// msiexec normally stops this service within seconds; while we are still
	// here, a finished log means it failed before getting that far.
	deadline := time.After(msiWait)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			return nil
		case <-tick.C:
		}
		b, err := os.ReadFile(logPath)
		if err != nil {
			continue
		}
		if exit, ok := parseMSILog(b); ok {
			if err := msiResult(exit, ok); err != nil {
				_ = os.Remove(w.markerPath())
				return err
			}
			return nil
		}
	}
}

// startMSI runs msiexec outside this service's process tree and job.
func startMSI(msi, logPath string) error {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "msiexec.exe")
	start := func(flags uint32) error {
		cmd := exec.Command(exe, msiArgs(msi, logPath)...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		err = start(base) // the job forbids breakaway
	}
	return err
}

func (w *winInstaller) checkAuthenticode(payload string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	runningValid, runningThumb := authenticode(exe)
	if !runningValid {
		return sigPolicy(false, false, false)
	}
	payloadValid, payloadThumb := authenticode(payload)
	return sigPolicy(true, payloadValid, payloadThumb != "" && payloadThumb == runningThumb)
}

// authenticode asks PowerShell for a file's signature status and signer;
// the path travels in the environment, never in the script.
func authenticode(path string) (valid bool, thumbprint string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := `$s = Get-AuthenticodeSignature -LiteralPath $env:RC_SIG_PATH; "$($s.Status)|$($s.SignerCertificate.Thumbprint)"`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "RC_SIG_PATH="+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false, ""
	}
	return parseAuthenticode(string(out))
}

// Watch is the post-update health check of the service (PROTOCOL §7 step 6).
func (w *winInstaller) Watch(ctx context.Context, acked <-chan struct{}, report func(proto.UpdateStatus)) {
	m, err := ReadMarker(w.markerPath())
	if err != nil {
		return
	}
	upSince := time.Now()
	for {
		isAcked := closed(acked)
		up := isAcked || w.probe == nil || w.probe(ctx)
		now := time.Now()
		if !up {
			upSince = now
		}
		action := DecideHealth(HealthInput{Marker: m, Running: w.version, Acked: isAcked, ServerUp: up, UserPresent: true,
			Since: now.Sub(upSince), Now: now})
		if action == HealthReportFailed {
			b, err := os.ReadFile(filepath.Join(w.updatesDir(), msiLogName(m.To)))
			if awaitInstaller(b, err == nil, now.Sub(m.AppliedAt)) {
				action = HealthWait // still the old build: msiexec may be rolling back
			}
		}
		w.log.Debug("update health", "action", action, "from", m.From, "to", m.To, "starts", m.Starts)
		switch action {
		case HealthHealthy:
			w.log.Info("update healthy", "version", m.To)
			w.keepInstaller(m.To)
			_ = os.Remove(w.markerPath())
			return
		case HealthAbandon:
			_ = os.Remove(w.markerPath())
			return
		case HealthReportFailed, HealthReportRolledBack:
			st := proto.UpdateStatus{State: proto.UpdateRolledBack, From: m.From, Version: m.To, Error: m.Error}
			if action == HealthReportFailed {
				st.State, st.Error = proto.UpdateFailed, w.failedReason(m.To)
			}
			_ = AddSkip(w.dataDir, m.To) // before hello_ack can start another check
			if !waitClosed(ctx, acked) {
				return
			}
			report(st)
			_ = os.Remove(w.markerPath())
			w.prune()
			return
		case HealthRollback:
			w.rollback(m)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-acked:
		case <-time.After(healthEvery):
		}
	}
}

func (w *winInstaller) rollback(m Marker) {
	reason := rollbackReason(m)
	_ = AddSkip(w.dataDir, m.To)
	if m.Previous == "" {
		w.log.Error("update unhealthy and no previous installer kept; staying on it", "version", m.To, "reason", reason)
		_ = os.Remove(w.markerPath())
		return
	}
	w.log.Error("update unhealthy; reinstalling the previous build", "version", m.To, "to", m.From, "reason", reason)
	m.RolledBack, m.Error = true, reason
	if err := WriteMarker(w.markerPath(), m); err != nil {
		w.log.Error("rollback: marker", "err", err)
		return
	}
	if err := startMSI(m.Previous, filepath.Join(w.updatesDir(), "msi-rollback-"+m.From+".log")); err != nil {
		w.log.Error("rollback: start msiexec", "err", err)
		_ = os.Remove(w.markerPath())
	}
}

func (w *winInstaller) failedReason(version string) string {
	b, err := os.ReadFile(filepath.Join(w.updatesDir(), msiLogName(version)))
	if err != nil {
		return "installer did not run"
	}
	if reason := msiFailure(b); reason != "" {
		return reason
	}
	return "installer finished but " + version + " is not running"
}

// keepInstaller makes the running build's MSI the rollback target and
// drops everything else.
func (w *winInstaller) keepInstaller(version string) {
	src := filepath.Join(w.updatesDir(), msiName(version))
	if _, err := os.Stat(src); err == nil {
		if err := os.Rename(src, filepath.Join(w.updatesDir(), installedMSI)); err != nil {
			w.log.Warn("update: keep installer", "err", err)
		}
	}
	w.prune(msiLogName(version))
}

func (w *winInstaller) prune(keep ...string) {
	entries, err := os.ReadDir(w.updatesDir())
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	for _, n := range pruneInstallers(names, keep...) {
		_ = os.Remove(filepath.Join(w.updatesDir(), n))
	}
}
