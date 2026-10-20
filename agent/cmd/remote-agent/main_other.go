//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"

	"github.com/ruolez/remotectl/agent/internal/update"
)

// platformMain lets an OS take over argument handling (Windows service and
// session-helper modes live in main_windows.go). On macOS:
//
//	remote-agent update-daemon   the root update daemon (launchd com.remotectl.updater)
func platformMain(args []string) bool {
	if runtime.GOOS != "darwin" || len(args) == 0 || args[0] != "update-daemon" {
		return false
	}
	var w io.Writer = os.Stderr
	if f, err := openLogFile(update.SystemDaemonLog, maxLogSize); err == nil {
		w = io.MultiWriter(os.Stderr, f)
	}
	log := slog.New(slog.NewTextHandler(w, nil))
	// No signal context: a SIGTERM must not kill a running installer (launchd
	// leaves the process group alone, AbandonProcessGroup).
	if err := update.RunSystemDaemon(context.Background(), log); err != nil {
		log.Error("update daemon", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
