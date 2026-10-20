//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClaimInstanceRefusesASecondCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
	first, err := claimInstance(path, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file holds %q, want this pid", got)
	}
	if _, err := claimInstance(path, false, nil, time.Second); !errors.Is(err, errAlreadyRunning) {
		t.Fatalf("second claim: err = %v, want errAlreadyRunning", err)
	}
	first()
	again, err := claimInstance(path, false, nil, time.Second)
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	again()
}

// The launchd job (the build the installer just put in place) stops a copy
// that is already running instead of giving way to it.
func TestClaimInstanceTakesOverAsTheLaunchdJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
	holder, err := claimInstance(path, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var stopped []int
	stop := func(pid int) {
		stopped = append(stopped, pid)
		holder() // the old copy exits, releasing its lock
	}
	release, err := claimInstance(path, true, stop, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(stopped) != 1 || stopped[0] != os.Getpid() {
		t.Fatalf("stopped %v, want the holder's pid once", stopped)
	}
}

func TestClaimInstanceGivesUpOnAHolderThatStays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
	holder, err := claimInstance(path, false, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer holder()
	if _, err := claimInstance(path, true, func(int) {}, 200*time.Millisecond); !errors.Is(err, errAlreadyRunning) {
		t.Fatalf("err = %v, want errAlreadyRunning", err)
	}
}
