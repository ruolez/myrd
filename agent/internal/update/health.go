package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Post-update health rules (docs/PROTOCOL.md §7 "Agent flow", step 6).
const (
	HealthWindow = 5 * time.Minute // the new build must reach hello_ack within this
	MaxStarts    = 4               // starts without hello_ack that count as a crash loop
	AbandonAfter = 24 * time.Hour  // stop watching a marker this old
)

type HealthAction int

const (
	HealthWait HealthAction = iota
	HealthHealthy
	HealthRollback
	HealthReportFailed     // the install never took: still on From
	HealthReportRolledBack // the restored build reports the rollback
	HealthAbandon
)

func (a HealthAction) String() string {
	return [...]string{"wait", "healthy", "rollback", "report_failed", "report_rolled_back", "abandon"}[a]
}

// Marker records an install awaiting its health check.
type Marker struct {
	ID         string    `json:"id,omitempty"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Previous   string    `json:"previous,omitempty"` // installer or backup to restore
	AppliedAt  time.Time `json:"applied_at"`
	Starts     int       `json:"starts"`
	WatchFrom  time.Time `json:"watch_from,omitempty"` // start of the current server-up, user-present stretch
	RolledBack bool      `json:"rolled_back,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type HealthInput struct {
	Marker      Marker
	Running     string // version running now
	Acked       bool   // the running build reached hello_ack
	ServerUp    bool
	UserPresent bool          // macOS: a console user (the agent only runs in a GUI login)
	Since       time.Duration // how long the server has been up and a user present, continuously, for this build
	Now         time.Time
}

// DecideHealth applies the rollback rules: roll back only a build that is
// running, was never acknowledged and either failed to reach hello_ack for
// HealthWindow while it could have, or crash-looped while the server was up.
func DecideHealth(in HealthInput) HealthAction {
	m := in.Marker
	switch {
	case in.Running == m.From && m.From != m.To:
		if m.RolledBack {
			return HealthReportRolledBack
		}
		return HealthReportFailed
	case in.Running != m.To:
		return HealthAbandon
	case in.Acked:
		return HealthHealthy
	case in.Now.Sub(m.AppliedAt) >= AbandonAfter:
		return HealthAbandon
	case m.RolledBack, !in.ServerUp:
		return HealthWait
	case m.Starts >= MaxStarts:
		return HealthRollback
	case in.UserPresent && in.Since >= HealthWindow:
		return HealthRollback
	}
	return HealthWait
}

func ReadMarker(path string) (Marker, error) {
	var m Marker
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func WriteMarker(path string, m Marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
