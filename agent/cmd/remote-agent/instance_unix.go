//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var errAlreadyRunning = errors.New("another copy of the agent is already running")

// claimInstance makes this process the only agent for its data dir (two
// copies share the device identity and keep replacing each other's server
// connection). It holds an exclusive flock on path while the agent runs.
// When the lock is taken, takeover stops the holder (stop gets its pid) and
// waits up to wait for it to go; otherwise it returns errAlreadyRunning.
func claimInstance(path string, takeover bool, stop func(pid int), wait time.Duration) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := func() bool { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil }
	if !lock() {
		b, _ := os.ReadFile(path)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if !takeover || pid <= 0 {
			f.Close()
			return nil, fmt.Errorf("%w (pid %d)", errAlreadyRunning, pid)
		}
		stop(pid)
		deadline := time.Now().Add(wait)
		for !lock() {
			if time.Now().After(deadline) {
				f.Close()
				return nil, fmt.Errorf("%w (pid %d did not stop)", errAlreadyRunning, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// stopAgent ends an older copy: SIGTERM, then SIGKILL if it lingers.
func stopAgent(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	time.AfterFunc(6*time.Second, func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}

// launchAgentLabel is the macOS LaunchAgent job (BUNDLE_ID, packaging/macos/product.env).
const launchAgentLabel = "com.remotectl.agent"

// singleInstance claims the agent lock in dataDir. The LaunchAgent job takes
// over from a copy started some other way (e.g. an old build opened from
// Finder that outlived an update); any other second copy is refused.
func singleInstance(dataDir string) (release func(), err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	takeover := os.Getenv("XPC_SERVICE_NAME") == launchAgentLabel
	return claimInstance(filepath.Join(dataDir, "agent.lock"), takeover, stopAgent, 10*time.Second)
}
