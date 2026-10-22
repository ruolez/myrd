// Package config holds the agent's persisted enrollment state and runtime
// settings (flags/env).
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Settings are the runtime knobs from flags and RC_* environment variables.
type Settings struct {
	ServerURL   string
	DataDir     string
	EnrollToken string
	ConsoleAddr string   // device console listen addr (Linux demo device)
	UDPPort     int      // single ICE UDP port (0 = ephemeral)
	NAT1To1IPs  []string // extra host candidate IPs (Docker Desktop: 127.0.0.1)
	Hostname    string
}

// FromEnv fills Settings from RC_* variables with OS defaults.
func FromEnv() Settings {
	s := Settings{
		ServerURL:   os.Getenv("RC_SERVER_URL"),
		DataDir:     os.Getenv("RC_DATA_DIR"),
		EnrollToken: os.Getenv("RC_ENROLL_TOKEN"),
		ConsoleAddr: os.Getenv("RC_CONSOLE_ADDR"),
		Hostname:    os.Getenv("RC_HOSTNAME"),
	}
	if s.DataDir == "" {
		s.DataDir = DefaultDataDir()
	}
	if p, err := strconv.Atoi(os.Getenv("RC_UDP_PORT")); err == nil {
		s.UDPPort = p
	}
	for _, ip := range strings.Split(os.Getenv("RC_NAT1TO1_IPS"), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			s.NAT1To1IPs = append(s.NAT1To1IPs, ip)
		}
	}
	if s.Hostname == "" {
		s.Hostname, _ = os.Hostname()
	}
	return s
}

// DefaultDataDir is per-OS: macOS ~/Library/Application Support/RemoteAgent,
// Windows %ProgramData%\RemoteAgent, Linux /var/lib/remotectl (or
// $XDG_STATE_HOME/remotectl for non-root).
func DefaultDataDir() string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "RemoteAgent")
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "RemoteAgent")
	default:
		if os.Geteuid() == 0 {
			return "/var/lib/remotectl"
		}
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			return filepath.Join(x, "remotectl")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "state", "remotectl")
	}
}

// State is persisted as config.json in the data dir.
type State struct {
	ServerURL string `json:"server_url"`
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
}

func (s State) Enrolled() bool { return s.DeviceID != "" }

func statePath(dataDir string) string { return filepath.Join(dataDir, "config.json") }

// Load returns the zero State when none is stored.
func Load(dataDir string) (State, error) {
	var st State
	b, err := os.ReadFile(statePath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func Save(dataDir string, st State) error { return writeJSON(dataDir, statePath(dataDir), st) }

func writeJSON(dataDir, path string, v any) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Remove(dataDir string) error {
	err := os.Remove(statePath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Prefs are choices made on the device itself (tray, device console),
// persisted as prefs.json in the data dir. The zero value is the default.
type Prefs struct {
	// AskNewBrowsers prompts on the device before a browser it has not
	// approved connects. Off: the first session from a browser is approved
	// and remembered for its account, as "Allow and remember" would.
	AskNewBrowsers bool `json:"ask_new_browsers"`
}

// PrefsPath is prefs.json in the data dir.
func PrefsPath(dataDir string) string { return filepath.Join(dataDir, "prefs.json") }

// LoadPrefs returns the defaults when none are stored.
func LoadPrefs(dataDir string) (Prefs, error) {
	var p Prefs
	b, err := os.ReadFile(PrefsPath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

func SavePrefs(dataDir string, p Prefs) error { return writeJSON(dataDir, PrefsPath(dataDir), p) }
