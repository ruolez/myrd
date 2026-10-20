//go:build darwin

package tray

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// PromptText asks the local user for a line of text (first-run enrollment
// token) with a native dialog. ok is false when the user cancels or the
// dialog times out. Arguments travel as argv, never interpolated into the
// AppleScript source.
func PromptText(title, message, button string, timeout time.Duration) (text string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	script := []string{
		"on run argv",
		`set r to display dialog (item 2 of argv) default answer "" with title (item 1 of argv) ` +
			`buttons {"Cancel", (item 3 of argv)} default button 2 cancel button 1 with icon note`,
		"return text returned of r",
		"end run",
	}
	args := []string{}
	for _, l := range script {
		args = append(args, "-e", l)
	}
	args = append(args, title, message, button)
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) || ctx.Err() != nil {
			return "", false, nil // cancelled (-128) or timed out
		}
		return "", false, err
	}
	return strings.TrimSpace(string(out)), true, nil
}
