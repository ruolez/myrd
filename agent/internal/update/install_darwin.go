//go:build darwin

package update

import "time"

// SystemUpdaterDir is the root update daemon's directory (docs/PROTOCOL.md §7).
const SystemUpdaterDir = "/Library/Application Support/RemoteAgent/Updater"

func newDefaultInstaller(o Options) Installer {
	return &macInstaller{updaterDir: SystemUpdaterDir, dataDir: o.DataDir, version: o.Version, log: o.Log,
		poll: 2 * time.Second, answerWithin: 2 * time.Minute, finishWithin: 30 * time.Minute}
}

// RequestRestart asks the root update daemon to restart this Mac in a moment
// (control `power`).
func RequestRestart() error { return requestRestart(SystemUpdaterDir, 1500*time.Millisecond) }
