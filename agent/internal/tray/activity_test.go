package tray

import (
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/files"
)

var t0 = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func act(xfer uint32, dir, state string, bytes, size int64, started, updated time.Duration) files.Activity {
	return files.Activity{Xfer: xfer, Direction: dir, Name: "f" + string(rune('0'+xfer)) + ".txt", State: state,
		Bytes: bytes, Size: size, Started: t0.Add(started), Updated: t0.Add(updated)}
}

func TestActivityLine(t *testing.T) {
	cases := []struct {
		a    files.Activity
		want string
	}{
		{act(1, files.DirUpload, files.StateActive, 42, 100, 0, 0), "Receiving f1.txt (42%)"},
		{act(1, files.DirDownload, files.StateActive, 1, 3, 0, 0), "Sending f1.txt (33%)"},
		{act(1, files.DirUpload, files.StateActive, 0, 0, 0, 0), "Receiving f1.txt"},
		{act(1, files.DirUpload, files.StateOK, 5, 5, 0, 0), "Received f1.txt"},
		{act(1, files.DirDownload, files.StateOK, 5, 5, 0, 0), "Sent f1.txt"},
		{act(1, files.DirDownload, files.StateCancelled, 1, 5, 0, 0), "Cancelled f1.txt"},
		{act(1, files.DirUpload, files.StateError, 1, 5, 0, 0), "Failed to receive f1.txt"},
		{act(1, files.DirDownload, files.StateError, 1, 5, 0, 0), "Failed to send f1.txt"},
	}
	for _, c := range cases {
		if got := ActivityLine(c.a); got != c.want {
			t.Errorf("ActivityLine(%+v) = %q, want %q", c.a, got, c.want)
		}
	}
}

func TestSummary(t *testing.T) {
	sec := time.Second
	cases := []struct {
		name string
		acts []files.Activity
		now  time.Duration
		want string
	}{
		{"nothing", nil, 0, ""},
		{"one running", []files.Activity{act(1, files.DirUpload, files.StateActive, 1, 2, 0, 0)}, sec, "Receiving f1.txt (50%)"},
		{"running beats results", []files.Activity{
			act(1, files.DirUpload, files.StateOK, 2, 2, 0, sec),
			act(2, files.DirDownload, files.StateActive, 0, 2, sec, sec)}, 2 * sec, "Sending f2.txt (0%)"},
		{"newest running + count", []files.Activity{
			act(1, files.DirUpload, files.StateActive, 0, 2, 0, 0),
			act(2, files.DirUpload, files.StateActive, 0, 2, 2*sec, 2*sec),
			act(3, files.DirUpload, files.StateActive, 0, 2, sec, sec)}, 3 * sec, "Receiving f2.txt (0%) + 2 more"},
		{"latest result lingers", []files.Activity{
			act(1, files.DirUpload, files.StateOK, 2, 2, 0, sec),
			act(2, files.DirDownload, files.StateError, 0, 2, 0, 3*sec)}, 5 * sec, "Failed to send f2.txt"},
		{"result expired", []files.Activity{act(1, files.DirUpload, files.StateOK, 2, 2, 0, sec)}, sec + ResultLinger, ""},
	}
	for _, c := range cases {
		if got := Summary(c.acts, t0.Add(c.now)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestActivityFeed(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	var notes []string
	now := t0
	f := NewActivityFeed(func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() },
		func(title, body string) { notes = append(notes, body) })
	f.now = func() time.Time { return now }

	f.Add(act(1, files.DirUpload, files.StateActive, 0, 10, 0, 0))
	f.Add(act(1, files.DirUpload, files.StateActive, 5, 10, 0, time.Second))
	f.Add(act(2, files.DirDownload, files.StateActive, 0, 10, time.Second, time.Second))
	if len(notes) != 1 || notes[0] != "Receiving f1.txt" {
		t.Fatalf("notifications %v, want one (coalesced)", notes)
	}
	now = t0.Add(2 * time.Second)
	f.Add(act(1, files.DirUpload, files.StateOK, 10, 10, 0, 2*time.Second))
	now = t0.Add(3 * time.Second)
	f.Add(act(2, files.DirDownload, files.StateOK, 10, 10, time.Second, 3*time.Second))
	if f.Line() != "Sent f2.txt" {
		t.Fatalf("line %q", f.Line())
	}
	now = t0.Add(3*time.Second + ResultLinger)
	f.refresh()
	if f.Line() != "" {
		t.Fatalf("line after linger %q", f.Line())
	}
	now = now.Add(NotifyEvery)
	f.Add(act(3, files.DirUpload, files.StateActive, 0, 10, 20*time.Second, 20*time.Second))
	if len(notes) != 2 {
		t.Fatalf("notifications %v", notes)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"Receiving f1.txt (0%)", "Receiving f1.txt (50%)", "Sending f2.txt (0%) + 1 more", "Sending f2.txt (0%)", "Sent f2.txt", "", "Receiving f3.txt (0%)"}
	if len(lines) != len(want) {
		t.Fatalf("lines %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines %q, want %q", lines, want)
		}
	}
}
