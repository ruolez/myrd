package update

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/ruolez/remotectl/proto"
)

// macOS update daemon contract (docs/PROTOCOL.md §7 "macOS"): the user agent
// drops requests into the daemon's inbox and reads its status.json.

const (
	OpInstall = "install"
	OpHealthy = "healthy"
	// OpRestart restarts the Mac (control `power`). Taken only from the
	// console user, who could restart it from the Apple menu anyway.
	OpRestart = "restart"

	MaxRequestBytes = 16 << 10
)

// Daemon status states.
const (
	DaemonVerifying  = "verifying"
	DaemonInstalling = "installing"
	DaemonInstalled  = "installed"
	DaemonFailed     = "failed"
	DaemonRolledBack = "rolled_back"
	DaemonHealthy    = "healthy"
)

type ReqManifest struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

type Request struct {
	V        int          `json:"v"`
	Op       string       `json:"op"`
	ID       string       `json:"id"`
	Manifest *ReqManifest `json:"manifest,omitempty"`
	Payload  string       `json:"payload,omitempty"`
	Version  string       `json:"version,omitempty"`
}

type DaemonStatus struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	State string `json:"state"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Error string `json:"error,omitempty"`
	At    string `json:"at,omitempty"`
}

var reRequestID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// userPayload is where the agent of the user with home userHome downloads
// version; the daemon reads payloads from nowhere else.
func userPayload(userHome, version string) string {
	return filepath.Join(userHome, "Library", "Application Support", "RemoteAgent", "updates",
		"remote-agent-"+version+proto.ExtForOS("darwin"))
}

// ValidateRequest checks a request read from the inbox against what the
// daemon trusts: the compiled-in release key, the installed version and the
// requesting user's home.
func ValidateRequest(req Request, installedVersion, pubKey, userHome string) error {
	switch {
	case req.V != 1:
		return fmt.Errorf("request version %d", req.V)
	case !reRequestID.MatchString(req.ID):
		return fmt.Errorf("bad request id %q", req.ID)
	}
	switch req.Op {
	case OpHealthy:
		if !proto.ValidVersion(req.Version) {
			return fmt.Errorf("bad version %q", req.Version)
		}
		return nil
	case OpRestart:
		return nil
	case OpInstall:
	default:
		return fmt.Errorf("unknown op %q", req.Op)
	}
	m := req.Manifest
	switch {
	case m == nil:
		return errors.New("install request without a manifest")
	case !proto.ValidVersion(m.Version):
		return fmt.Errorf("bad version %q", m.Version)
	case m.OS != "darwin" || (m.Arch != "universal" && m.Arch != runtime.GOARCH):
		return fmt.Errorf("release is for %s/%s", m.OS, m.Arch)
	case proto.CompareVersions(m.Version, installedVersion) <= 0:
		return fmt.Errorf("%s is not newer than the installed %s", m.Version, installedVersion)
	case m.Size <= 0 || m.Size > MaxPayload:
		return fmt.Errorf("release size %d out of range", m.Size)
	}
	if err := proto.VerifyRelease(pubKey, m.Version, m.OS, m.Arch, m.SHA256, m.Signature); err != nil {
		return err
	}
	if userHome == "" {
		return errors.New("no home directory for the requesting user")
	}
	if want := userPayload(userHome, m.Version); req.Payload != want {
		return fmt.Errorf("payload must be %s", want)
	}
	return nil
}

// Agent-side startup actions for the daemon's last status.
const (
	startupHealthy = "healthy" // send a healthy request after hello_ack
	startupReport  = "report"  // report failed / rolled_back once
)

func startupAction(st DaemonStatus, running, reportedID string) string {
	switch st.State {
	case DaemonVerifying, DaemonInstalling, DaemonInstalled: // postinstall may start us before "installed"
		if st.To == running {
			return startupHealthy
		}
	case DaemonFailed, DaemonRolledBack:
		if st.ID != reportedID {
			return startupReport
		}
	}
	return ""
}
