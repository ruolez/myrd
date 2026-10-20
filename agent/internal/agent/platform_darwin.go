package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ruolez/remotectl/agent/internal/tray"
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
}
