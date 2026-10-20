package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

func TestSpreadWindow(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{{0, 0}, {1, 3 * time.Second}, {20, time.Minute}, {200, 10 * time.Minute}, {201, 10 * time.Minute}, {100000, 10 * time.Minute}}
	for _, c := range cases {
		if got := spreadWindow(c.n, 3*time.Second); got != c.want {
			t.Errorf("spreadWindow(%d) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestSpreadDelays(t *testing.T) {
	low := func(int64) int64 { return 0 }
	high := func(n int64) int64 { return n - 1 }
	cases := []struct {
		name   string
		n      int
		window time.Duration
		rnd    func(int64) int64
		want   []time.Duration
	}{
		{"none", 0, time.Minute, high, []time.Duration{}},
		{"slot starts", 3, 9 * time.Second, low, []time.Duration{0, 3 * time.Second, 6 * time.Second}},
		{"slot ends stay inside the window", 3, 9 * time.Second, high,
			[]time.Duration{3*time.Second - 1, 6*time.Second - 1, 9*time.Second - 1}},
		{"zero window sends at once", 2, 0, func(int64) int64 { panic("rnd called with an empty slot") }, []time.Duration{0, 0}},
		{"single device", 1, 3 * time.Second, func(n int64) int64 { return n / 2 }, []time.Duration{1500 * time.Millisecond}},
	}
	for _, c := range cases {
		if got := spreadDelays(c.n, c.window, c.rnd); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: spreadDelays = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAuditEventFor(t *testing.T) {
	cases := []struct {
		prev, next string
		want       string
		ok         bool
	}{
		{"", proto.UpdateChecking, "", false},
		{proto.UpdateChecking, proto.UpdateDownloading, "", false},
		{proto.UpdateDownloading, proto.UpdateWaitingIdle, "", false},
		{proto.UpdateWaitingIdle, proto.UpdateInstalling, "agent.update.started", true},
		{proto.UpdateDownloading, proto.UpdateInstalling, "agent.update.started", true},
		{proto.UpdateInstalling, proto.UpdateInstalling, "", false},
		{proto.UpdateChecking, proto.UpdateUpToDate, "", false},
		{proto.UpdateInstalling, proto.UpdateFailed, "agent.update.failed", true},
		{proto.UpdateFailed, proto.UpdateFailed, "agent.update.failed", true},
		{"", proto.UpdateRolledBack, "agent.update.rolled_back", true},
	}
	for _, c := range cases {
		got, ok := auditEventFor(c.prev, proto.UpdateStatus{State: c.next})
		if got != c.want || ok != c.ok {
			t.Errorf("auditEventFor(%q, %q) = %q, %v; want %q, %v", c.prev, c.next, got, ok, c.want, c.ok)
		}
	}
}

func TestHelloOutcome(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	recent, old := now.Add(-time.Minute), now.Add(-16*time.Minute)
	type want struct {
		st    proto.UpdateStatus
		event string
		ok    bool
	}
	cases := []struct {
		name                 string
		state, from, version string
		at                   *time.Time
		running              string
		want                 want
	}{
		{"new version reported", proto.UpdateInstalling, "0.2.0", "0.3.0", &recent, "0.3.0",
			want{proto.UpdateStatus{State: proto.UpdateUpToDate, From: "0.2.0", Version: "0.3.0"}, "agent.update.succeeded", true}},
		{"old version after the install window", proto.UpdateInstalling, "0.2.0", "0.3.0", &old, "0.2.0",
			want{proto.UpdateStatus{State: proto.UpdateFailed, From: "0.2.0", Version: "0.3.0", Error: "still on 0.2.0 after install"},
				"agent.update.failed", true}},
		{"old version while installing", proto.UpdateInstalling, "0.2.0", "0.3.0", &recent, "0.2.0", want{}},
		{"old version with no timestamp", proto.UpdateInstalling, "0.2.0", "0.3.0", nil, "0.2.0", want{}},
		{"some other version", proto.UpdateInstalling, "0.2.0", "0.3.0", &old, "0.2.5", want{}},
		{"not installing", proto.UpdateDownloading, "0.2.0", "0.3.0", &old, "0.3.0", want{}},
		{"no target", proto.UpdateInstalling, "", "", &old, "", want{}},
		{"failed, then installed by hand", proto.UpdateFailed, "0.3.0", "0.3.1", &old, "0.3.1",
			want{proto.UpdateStatus{State: proto.UpdateUpToDate, From: "0.3.0", Version: "0.3.1"}, "", true}},
		{"rolled back, later on a newer build", proto.UpdateRolledBack, "0.3.0", "0.3.1", &old, "0.3.2",
			want{proto.UpdateStatus{State: proto.UpdateUpToDate, From: "0.3.0", Version: "0.3.1"}, "", true}},
		{"failed and still on the old build", proto.UpdateFailed, "0.3.0", "0.3.1", &old, "0.3.0", want{}},
	}
	for _, c := range cases {
		st, ev, ok := helloOutcome(c.state, c.from, c.version, c.at, c.running, now)
		if got := (want{st, ev, ok}); got != c.want {
			t.Errorf("%s: helloOutcome = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestUpdateTargets(t *testing.T) {
	pub := &time.Time{}
	rels := []store.Release{
		{ID: "mac2", Version: "0.2.0", OS: "darwin", Arch: "universal", RolloutPct: 100, PublishedAt: pub},
		{ID: "mac3", Version: "0.3.0", OS: "darwin", Arch: "universal", RolloutPct: 0, PublishedAt: pub},
		{ID: "win2", Version: "0.2.0", OS: "windows", Arch: "amd64", RolloutPct: 100, PublishedAt: pub},
	}
	devs := []store.Device{
		{ID: "a", OS: "darwin", Arch: "arm64", AgentVersion: "0.1.0"},
		{ID: "b", OS: "darwin", Arch: "arm64", AgentVersion: "0.3.0"},
		{ID: "c", OS: "darwin", Arch: "amd64", AgentVersion: "0.1.0", UpdateState: proto.UpdateFailed, UpdateVersion: "0.3.0"},
		{ID: "d", OS: "darwin", Arch: "amd64", AgentVersion: "0.1.0", UpdateState: proto.UpdateRolledBack, UpdateVersion: "0.2.0"},
		{ID: "e", OS: "darwin", Arch: "amd64", AgentVersion: "0.1.0", UpdateState: proto.UpdateFailed, UpdateVersion: "0.1.5"},
		{ID: "f", OS: "windows", Arch: "amd64", AgentVersion: "0.1.0"},
		{ID: "g", OS: "linux", Arch: "amd64", AgentVersion: "0.1.0"},
	}
	cases := []struct {
		name      string
		explicit  bool
		releaseID string
		want      []string
	}{
		{"explicit: newest for each, minus failed at that version", true, "", []string{"a", "d", "e", "f"}},
		{"cohort: 0.3.0 at 0% falls back to 0.2.0", false, "", []string{"a", "c", "e", "f"}},
		{"only one release's devices", false, "mac2", []string{"a", "c", "e"}},
		{"release nobody is offered", false, "mac3", []string{}},
	}
	for _, c := range cases {
		if got := updateTargets(devs, rels, c.explicit, c.releaseID); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: updateTargets = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 64, "short"},
		{"abcdef", 3, "abc"},
		{"aé", 2, "a"},
		{"bad\xffbyte", 64, "badbyte"},
	}
	for _, c := range cases {
		if got := cleanText(c.in, c.n); got != c.want {
			t.Errorf("cleanText(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
