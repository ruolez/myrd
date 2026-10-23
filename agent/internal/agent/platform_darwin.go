package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ruolez/remotectl/agent/internal/tray"
	"github.com/ruolez/remotectl/agent/internal/update"
)

func init() {
	promptEnrollToken = func(ctx context.Context, message string) (string, bool) {
		tok, ok, err := tray.PromptText("RemoteAgent", message, "Enroll", 10*time.Minute)
		return tok, ok && err == nil && ctx.Err() == nil
	}
	uninstallAction = func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		// RemoteAgent.app/Contents/MacOS/remote-agent → Contents/Resources/uninstall.sh
		script := filepath.Join(filepath.Dir(exe), "..", "Resources", "uninstall.sh")
		if _, err := os.Stat(script); err == nil {
			_ = exec.Command("/bin/sh", script).Start()
		}
	}
	requestPermissionsOnStart = true
	// The pkg's scripts restart the LaunchAgent; a copy started some other
	// way (e.g. from Finder) is left running the old build, paused for the
	// install. kickstart -k (re)starts the job on the new bundle.
	restartDevice = update.RequestRestart
	// Only a copy launchd runs as the LaunchAgent comes back after kickstart -k.
	restartAgent = func() error {
		target := launchAgentTarget()
		if out, err := exec.Command("launchctl", "print", target).CombinedOutput(); err != nil {
			return fmt.Errorf("not running as the LaunchAgent %s: %v: %s", target, err, out)
		}
		time.AfterFunc(1500*time.Millisecond, func() { _ = exec.Command("launchctl", "kickstart", "-k", target).Run() })
		return nil
	}
	restartIntoUpdate = func() {
		_ = exec.Command("launchctl", "kickstart", "-k", launchAgentTarget()).Run()
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
}

// launchAgentTarget is this user's LaunchAgent job (label = BUNDLE_ID in
// packaging/macos/product.env).
func launchAgentTarget() string { return fmt.Sprintf("gui/%d/com.remotectl.agent", os.Getuid()) }
