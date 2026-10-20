// Package update implements the signed auto-update flow (docs/PROTOCOL.md
// §7): the server picks the release (cohort included), the agent checks it
// is newer and for this OS/arch, verifies the offline release signature and
// the payload SHA-256, waits until no session is active (or ends them when
// forced), hands the installer to the OS and reports each step as
// update.status. Per-OS installers own the post-update health check.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

const (
	PollInterval = 6 * time.Hour
	PollJitter   = 30 * time.Minute
	MaxPayload   = 512 << 20
	idlePoll     = 5 * time.Second
)

// Trigger is why a check runs: Explicit (update.check from "Update now")
// skips the cohort and the skip-list, Force ends sessions instead of waiting.
type Trigger struct {
	Explicit bool
	Force    bool
}

// Installer hands a verified payload to the OS installer. It returns once
// the install is under way or done; the running process is usually replaced.
type Installer interface {
	Install(ctx context.Context, payload string, m api.Manifest) error
}

// Health is implemented by installers that check a freshly installed build:
// Watch runs for the life of the updater, acked closes on the first
// hello_ack, and report sends update.status.
type Health interface {
	Watch(ctx context.Context, acked <-chan struct{}, report func(proto.UpdateStatus))
}

type Options struct {
	API           *api.Client
	Token         func(context.Context) (string, error)
	Version       string
	ReleasePubKey string // default proto.ReleasePublicKey
	DataDir       string
	GOOS, GOARCH  string // default runtime.GOOS / runtime.GOARCH
	Log           *slog.Logger

	Busy        func() bool              // a session is pending or active
	EndAll      func(reason string)      // ends every session (forced updates)
	OnStatus    func(proto.UpdateStatus) // state changes, at most one per StatusEvery (latest kept)
	Installer   Installer                // default per OS
	HealthProbe func(context.Context) bool
	HTTP        *http.Client // downloads
	IdlePoll    time.Duration
	StatusEvery time.Duration // default StatusEvery; negative = unpaced
}

type Updater struct {
	o Options

	mu        sync.Mutex
	running   bool
	cur       Trigger
	next      *Trigger      // arrived while an attempt runs
	force     chan struct{} // closed: the running attempt must not wait for idle
	forced    bool
	supersede chan struct{}
	handedOff bool // an install was handed to the OS; this process is on its way out

	pacer   *pacer
	healthy chan struct{}
	once    sync.Once
}

func New(o Options) *Updater {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ReleasePubKey == "" {
		o.ReleasePubKey = proto.ReleasePublicKey
	}
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	if o.Busy == nil {
		o.Busy = func() bool { return false }
	}
	if o.EndAll == nil {
		o.EndAll = func(string) {}
	}
	if o.OnStatus == nil {
		o.OnStatus = func(proto.UpdateStatus) {}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Minute}
	}
	if o.IdlePoll == 0 {
		o.IdlePoll = idlePoll
	}
	if o.HealthProbe == nil && o.API != nil {
		base := o.API.BaseURL
		o.HealthProbe = func(ctx context.Context) bool { return ProbeServer(ctx, base) }
	}
	if o.Installer == nil {
		o.Installer = newDefaultInstaller(o)
	}
	if o.StatusEvery == 0 {
		o.StatusEvery = StatusEvery
	}
	return &Updater{o: o, pacer: newPacer(o.StatusEvery, o.OnStatus), healthy: make(chan struct{})}
}

// ProbeServer reports whether GET <base>/healthz answers 200 within 5 s.
func ProbeServer(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Run starts the installer's health watch and polls every PollInterval ±
// PollJitter until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	if h, ok := u.o.Installer.(Health); ok {
		go h.Watch(ctx, u.healthy, u.emit)
	}
	for {
		d := PollInterval - PollJitter + time.Duration(rand.Int64N(int64(2*PollJitter)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
			u.CheckNow(ctx, Trigger{})
		}
	}
}

// HelloAcked tells the health watch that this build reached the server.
func (u *Updater) HelloAcked() { u.once.Do(func() { close(u.healthy) }) }

// CheckNow runs an update attempt and returns when it is over. A trigger
// arriving while one runs is folded into it: Force stops it waiting for
// idle, anything else queues one more check (which also supersedes an idle
// wait, so a newer release replaces the one waiting).
func (u *Updater) CheckNow(ctx context.Context, t Trigger) {
	u.mu.Lock()
	if u.handedOff {
		u.mu.Unlock()
		return
	}
	if u.running {
		if t.Force {
			u.forceLocked()
		}
		if !t.Force || (t.Explicit && !u.cur.Explicit) {
			n := t
			if u.next != nil {
				n = Trigger{Explicit: n.Explicit || u.next.Explicit, Force: n.Force || u.next.Force}
			}
			u.next = &n
			select {
			case u.supersede <- struct{}{}:
			default:
			}
		}
		u.mu.Unlock()
		return
	}
	u.running = true
	for {
		u.cur, u.force, u.forced, u.supersede = t, make(chan struct{}), false, make(chan struct{}, 1)
		if t.Force {
			u.forceLocked()
		}
		force, supersede := u.force, u.supersede
		u.mu.Unlock()

		if err := u.attempt(ctx, t, force, supersede); err != nil {
			u.o.Log.Warn("update not installed", "err", err)
		}

		u.mu.Lock()
		if u.handedOff || u.next == nil || ctx.Err() != nil {
			u.running, u.next = false, nil
			u.mu.Unlock()
			return
		}
		t, u.next = *u.next, nil
	}
}

func (u *Updater) forceLocked() {
	if !u.forced {
		u.forced = true
		close(u.force)
	}
}

func (u *Updater) emit(s proto.UpdateStatus) {
	if len(s.Error) > proto.MaxUpdateErrorLen {
		s.Error = s.Error[:proto.MaxUpdateErrorLen]
	}
	u.pacer.push(s)
}

func (u *Updater) status(state, version string, err error) {
	s := proto.UpdateStatus{State: state, From: u.o.Version, Version: version}
	if err != nil {
		s.Error = err.Error()
	}
	u.emit(s)
}

// errCorrupt marks payload problems worth skip-listing the version for.
type errCorrupt struct{ msg string }

func (e errCorrupt) Error() string { return e.msg }

func (u *Updater) attempt(ctx context.Context, t Trigger, force, supersede <-chan struct{}) error {
	if t.Explicit {
		u.status(proto.UpdateChecking, "", nil)
	}
	m, err := u.check(ctx, t)
	if err != nil {
		if t.Explicit {
			u.status(proto.UpdateFailed, "", err)
		}
		return err
	}
	if m == nil {
		if t.Explicit {
			u.status(proto.UpdateUpToDate, "", nil)
		}
		return nil
	}
	fail := func(err error, skip bool) error {
		if skip {
			if serr := AddSkip(u.o.DataDir, m.Version); serr != nil {
				u.o.Log.Warn("update: skip-list", "err", serr)
			}
		}
		u.status(proto.UpdateFailed, m.Version, err)
		return fmt.Errorf("%s: %w", m.Version, err)
	}
	if err := eligible(*m, u.o.Version, u.o.GOOS, u.o.GOARCH, LoadSkip(u.o.DataDir), t.Explicit); err != nil {
		if errors.Is(err, errSkipped) {
			return err
		}
		return fail(err, false)
	}
	if err := proto.VerifyRelease(u.o.ReleasePubKey, m.Version, m.OS, m.Arch, m.SHA256, m.Signature); err != nil {
		return fail(err, true)
	}

	u.status(proto.UpdateDownloading, m.Version, nil)
	payload, err := u.download(ctx, *m)
	if err != nil {
		var corrupt errCorrupt
		return fail(fmt.Errorf("download: %w", err), errors.As(err, &corrupt))
	}

	if u.o.Busy() {
		select {
		case <-force:
		default:
			u.status(proto.UpdateWaitingIdle, m.Version, nil)
		}
		tick := time.NewTicker(u.o.IdlePoll)
		err := waitIdle(ctx, u.o.Busy, force, supersede, u.o.EndAll, tick.C)
		tick.Stop()
		if err != nil {
			return err
		}
	}

	u.status(proto.UpdateInstalling, m.Version, nil)
	u.pacer.flush(ctx) // the process may not outlive the install
	u.mu.Lock()
	u.handedOff = true
	u.mu.Unlock()
	if err := u.o.Installer.Install(ctx, payload, *m); err != nil {
		u.mu.Lock()
		u.handedOff = false
		u.mu.Unlock()
		return fail(err, true)
	}
	u.o.Log.Info("update handed to the OS installer", "version", m.Version)
	return nil
}

func (u *Updater) check(ctx context.Context, t Trigger) (*api.Manifest, error) {
	if u.o.API == nil || u.o.Token == nil {
		return nil, errors.New("not connected")
	}
	tok, err := u.o.Token(ctx)
	if err != nil {
		return nil, err
	}
	return u.o.API.CheckUpdate(ctx, tok, u.o.GOOS, u.o.GOARCH, u.o.Version, t.Explicit)
}

var errSkipped = errors.New("skipped")

// eligible checks a server-picked release against this build; the
// signature is verified separately.
func eligible(m api.Manifest, running, goos, goarch string, skip map[string]bool, explicit bool) error {
	switch {
	case !proto.ValidVersion(m.Version):
		return fmt.Errorf("release version %q is not semantic", m.Version)
	case m.OS != goos || (m.Arch != goarch && m.Arch != "universal"):
		return fmt.Errorf("release is for %s/%s, this device is %s/%s", m.OS, m.Arch, goos, goarch)
	case proto.CompareVersions(m.Version, running) <= 0:
		return fmt.Errorf("%s is not newer than %s", m.Version, running)
	case m.Size <= 0 || m.Size > MaxPayload:
		return fmt.Errorf("release size %d out of range", m.Size)
	case skip[m.Version] && !explicit:
		return fmt.Errorf("%s failed on this device before; %w", m.Version, errSkipped)
	}
	return nil
}

// download fetches the payload into <DataDir>/updates, never reading more
// than the signed size, and checks its SHA-256. A complete earlier download
// of the same release is reused.
func (u *Updater) download(ctx context.Context, m api.Manifest) (string, error) {
	dir := filepath.Join(u.o.DataDir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "remote-agent-"+m.Version+proto.ExtForOS(m.OS))
	if hashFile(dst) == strings.ToLower(m.SHA256) {
		return dst, nil
	}
	src, err := api.ResolveURL(u.o.API.BaseURL, m.URL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.o.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > m.Size {
		return "", errCorrupt{fmt.Sprintf("larger than the signed size %d", m.Size)}
	}
	part := dst + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, m.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case n > m.Size:
		err = errCorrupt{fmt.Sprintf("larger than the signed size %d", m.Size)}
	case n < m.Size:
		err = fmt.Errorf("got %d of %d bytes", n, m.Size)
	case hex.EncodeToString(h.Sum(nil)) != strings.ToLower(m.SHA256):
		err = errCorrupt{"sha256 mismatch"}
	}
	if err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return dst, os.Rename(part, dst)
}

func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
