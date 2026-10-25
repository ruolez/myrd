package main

import (
	"flag"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"

	"github.com/ruolez/remotectl/agent/internal/capproc"
	"github.com/ruolez/remotectl/agent/internal/native"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/sysinfo"
)

// captureBackend is the native backend with capture and encoding in a worker
// process per session (capproc). RC_CAPTURE_WORKER=0 keeps them in this
// process.
func captureBackend(log *slog.Logger) platform.Backend {
	b := native.New()
	if os.Getenv("RC_CAPTURE_WORKER") == "0" || b.Name() == "none" {
		return b
	}
	exe, err := os.Executable()
	if err != nil {
		log.Warn("capture stays in this process", "err", err)
		return b
	}
	args := []string{"capture-worker", "-owner", strconv.Itoa(os.Getpid())}
	return capproc.New(b, capproc.Options{Spawn: capproc.Command(exe, args, log), OnRetire: releaseMemory, Log: log})
}

// releaseMemory gives back what a session left in this process's heaps.
func releaseMemory() {
	debug.FreeOSMemory()
	native.ReleaseMemory()
}

// cmdCaptureWorker is the capture process an agent spawns per session:
// commands on stdin, frames on stdout, its log on stderr (the agent relays
// it). It exits when stdin closes.
func cmdCaptureWorker(args []string) int {
	fs := flag.NewFlagSet("capture-worker", flag.ContinueOnError)
	owner := fs.Int("owner", 0, "pid of the agent it captures for")
	if fs.Parse(args) != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	native.SetLogger(log)
	wire, err := capproc.TakeStdout()
	if err != nil {
		log.Error("no stdout for frames", "err", err)
		return 1
	}
	if err := native.InitCaptureWorker(*owner); err != nil {
		log.Error("native init failed", "err", err)
		return 1
	}
	if err := capproc.Serve(os.Stdin, wire, native.New(), capproc.WorkerOptions{Memory: sysinfo.SelfMemory}); err != nil {
		log.Error("capture worker stopped", "err", err)
		return 1
	}
	return 0
}
