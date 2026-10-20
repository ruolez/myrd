// Package update implements the signed auto-update flow (plan: "Auto-update"):
// poll every 6 h with jitter and on reconnect, honor rollout_pct via a stable
// device bucket, verify the Ed25519 manifest signature (offline release key
// compiled into the agent) and the payload SHA-256, apply per OS, and roll
// back when the new build never reaches hello_ack.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

const (
	PollInterval   = 6 * time.Hour
	HealthDeadline = 60 * time.Second
	markerName     = "update-pending.json"
)

// Bucket is uint32_be(sha256(device_id)[0:4]) % 100.
func Bucket(deviceID string) int {
	h := sha256.Sum256([]byte(deviceID))
	return int(binary.BigEndian.Uint32(h[:4]) % 100)
}

// InCohort reports whether this device takes a release at rolloutPct.
func InCohort(deviceID string, rolloutPct int) bool { return Bucket(deviceID) < rolloutPct }

// VerifyManifest checks the offline release signature.
func VerifyManifest(m api.Manifest, releasePubB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(releasePubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("no valid release public key compiled in")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("bad manifest signature encoding")
	}
	if !ed25519.Verify(pub, proto.ReleaseManifestMessage(m.Version, m.OS, m.Arch, m.URL, m.SHA256), sig) {
		return errors.New("manifest signature mismatch")
	}
	return nil
}

// VerifyFile checks a downloaded payload's SHA-256.
func VerifyFile(path, sha256hex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sha256hex) {
		return fmt.Errorf("sha256 mismatch: got %s", got)
	}
	return nil
}

// Newer reports whether version a is strictly newer than b (dotted numeric,
// optional leading "v", pre-release suffixes ignored).
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// Marker records an applied update awaiting its health check.
type Marker struct {
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	Previous    string    `json:"previous"` // path of the kept previous build
	AppliedAt   time.Time `json:"applied_at"`
}

type Options struct {
	API           *api.Client
	Token         func(context.Context) (string, error)
	DeviceID      string
	Version       string
	ReleasePubKey string
	DataDir       string
	Log           *slog.Logger
	// Apply installs a verified payload and returns the path of the kept
	// previous build (for rollback). Default: per-OS (applyDefault).
	Apply func(payload string, m api.Manifest) (previous string, err error)
	// Rollback restores previous. Default: per-OS.
	Rollback func(previous string) error
}

type Updater struct {
	o       Options
	mu      sync.Mutex
	busy    bool
	healthy chan struct{}
	once    sync.Once
}

func New(o Options) *Updater {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Apply == nil {
		o.Apply = applyDefault
	}
	if o.Rollback == nil {
		o.Rollback = rollbackDefault
	}
	return &Updater{o: o, healthy: make(chan struct{})}
}

// Run polls until ctx ends. It also arms the post-update health check.
func (u *Updater) Run(ctx context.Context) {
	go u.healthCheck(ctx)
	for {
		jitter := time.Duration(rand.Int64N(int64(30 * time.Minute)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(PollInterval + jitter):
			u.CheckNow(ctx)
		}
	}
}

// HelloAcked marks the running build healthy (clears a pending marker).
func (u *Updater) HelloAcked() { u.once.Do(func() { close(u.healthy) }) }

func (u *Updater) markerPath() string { return filepath.Join(u.o.DataDir, markerName) }

func (u *Updater) healthCheck(ctx context.Context) {
	b, err := os.ReadFile(u.markerPath())
	if err != nil {
		return
	}
	var m Marker
	if json.Unmarshal(b, &m) != nil || m.ToVersion != u.o.Version {
		_ = os.Remove(u.markerPath())
		return
	}
	select {
	case <-u.healthy:
		u.o.Log.Info("update healthy", "version", m.ToVersion)
		_ = os.Remove(u.markerPath())
	case <-time.After(HealthDeadline):
		u.o.Log.Error("updated build never reached hello_ack; rolling back", "to", m.FromVersion)
		_ = os.Remove(u.markerPath())
		if err := u.o.Rollback(m.Previous); err != nil {
			u.o.Log.Error("rollback failed", "err", err)
		}
	case <-ctx.Done():
	}
}

// CheckNow asks the server for a newer release and installs it when this
// device is in the rollout cohort and every signature checks out.
func (u *Updater) CheckNow(ctx context.Context) {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.busy = false; u.mu.Unlock() }()

	if u.o.ReleasePubKey == "" {
		u.o.Log.Debug("auto-update disabled: no release key compiled in")
		return
	}
	tok, err := u.o.Token(ctx)
	if err != nil {
		return
	}
	m, err := u.o.API.CheckUpdate(ctx, tok, runtime.GOOS, runtime.GOARCH, u.o.Version)
	if err != nil || m == nil {
		return
	}
	if err := u.Install(ctx, *m); err != nil {
		u.o.Log.Warn("update not installed", "version", m.Version, "err", err)
	}
}

// Install verifies and applies m (exported for tests and `remote-agent update`).
func (u *Updater) Install(ctx context.Context, m api.Manifest) error {
	if !Newer(m.Version, u.o.Version) {
		return fmt.Errorf("%s is not newer than %s", m.Version, u.o.Version)
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		return fmt.Errorf("manifest for %s/%s", m.OS, m.Arch)
	}
	if !InCohort(u.o.DeviceID, m.RolloutPct) {
		return fmt.Errorf("not in rollout cohort (bucket %d >= %d%%)", Bucket(u.o.DeviceID), m.RolloutPct)
	}
	if err := VerifyManifest(m, u.o.ReleasePubKey); err != nil {
		return err
	}
	dir := filepath.Join(u.o.DataDir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload := filepath.Join(dir, fmt.Sprintf("remote-agent-%s%s", m.Version, payloadExt(m.URL)))
	if err := download(ctx, m.URL, payload); err != nil {
		return err
	}
	if err := VerifyFile(payload, m.SHA256); err != nil {
		_ = os.Remove(payload)
		return err
	}
	if err := verifyOSSignature(payload); err != nil {
		_ = os.Remove(payload)
		return err
	}
	b, _ := json.Marshal(Marker{FromVersion: u.o.Version, ToVersion: m.Version, AppliedAt: time.Now()})
	_ = os.WriteFile(u.markerPath(), b, 0o600)
	prev, err := u.o.Apply(payload, m)
	if err != nil {
		_ = os.Remove(u.markerPath())
		return err
	}
	b, _ = json.Marshal(Marker{FromVersion: u.o.Version, ToVersion: m.Version, Previous: prev, AppliedAt: time.Now()})
	return os.WriteFile(u.markerPath(), b, 0o600)
}

func payloadExt(url string) string {
	for _, ext := range []string{".msi", ".zip", ".pkg", ".tar.gz"} {
		if strings.HasSuffix(strings.ToLower(url), ext) {
			return ext
		}
	}
	return ".bin"
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 512<<20)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(dst+".part", dst)
}
