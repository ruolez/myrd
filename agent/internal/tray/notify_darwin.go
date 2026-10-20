//go:build darwin

package tray

import (
	"context"
	"os/exec"
	"time"
)

// Notify posts a Notification Center banner without blocking. Arguments
// travel as argv, never interpolated into the AppleScript source.
func Notify(title, body string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		args := []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run", title, body}
		_ = exec.CommandContext(ctx, "/usr/bin/osascript", args...).Run()
	}()
}
