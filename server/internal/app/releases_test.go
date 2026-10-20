package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/ruolez/remotectl/server/internal/store"
)

func TestPickRelease(t *testing.T) {
	pub := &time.Time{}
	rel := func(id, version, os, arch string, pct int, published bool) store.Release {
		r := store.Release{ID: id, Version: version, OS: os, Arch: arch, RolloutPct: pct}
		if published {
			r.PublishedAt = pub
		}
		return r
	}
	mac := []store.Release{
		rel("m030", "0.3.0", "darwin", "universal", 100, true),
		rel("m031", "0.3.1", "darwin", "universal", 50, true),
		rel("m032", "0.3.2", "darwin", "universal", 10, true),
		rel("m040", "0.4.0", "darwin", "universal", 100, false),
		rel("w035", "0.3.5", "windows", "amd64", 100, true),
	}
	equal := []store.Release{
		rel("uni", "0.3.0", "darwin", "universal", 100, true),
		rel("arm", "0.3.0", "darwin", "arm64", 100, true),
		rel("amd", "0.3.0", "darwin", "amd64", 100, true),
	}
	pre := []store.Release{
		rel("rc1", "0.3.0-rc.1", "windows", "amd64", 100, true),
		rel("rc2", "0.3.0-rc.2", "windows", "amd64", 100, true),
		rel("dev10", "0.3.0-dev.10", "windows", "amd64", 100, true),
	}
	cases := []struct {
		name              string
		rels              []store.Release
		os, arch, current string
		bucket            int
		explicit          bool
		want              string
	}{
		{"in every cohort takes the newest", mac, "darwin", "arm64", "0.2.0", 5, false, "m032"},
		{"outside the newest cohort falls back", mac, "darwin", "arm64", "0.2.0", 30, false, "m031"},
		{"outside both newer cohorts", mac, "darwin", "arm64", "0.2.0", 70, false, "m030"},
		{"bucket equal to rollout is outside", mac, "darwin", "arm64", "0.2.0", 10, false, "m031"},
		{"explicit ignores the cohort", mac, "darwin", "arm64", "0.2.0", 99, true, "m032"},
		{"unpublished is never offered", mac, "darwin", "arm64", "0.3.2", 0, true, ""},
		{"no downgrade", mac, "darwin", "amd64", "0.5.0", 0, true, ""},
		{"same version is not newer", mac, "darwin", "amd64", "0.3.2", 0, true, ""},
		{"fallback still has to be newer", mac, "darwin", "amd64", "0.3.1", 50, false, ""},
		{"os mismatch", mac, "linux", "amd64", "0.1.0", 0, true, ""},
		{"arch mismatch", mac, "windows", "arm64", "0.1.0", 0, true, ""},
		{"exact arch preferred at equal version", equal, "darwin", "arm64", "0.2.0", 0, false, "arm"},
		{"exact amd64 preferred", equal, "darwin", "amd64", "0.2.0", 0, false, "amd"},
		{"universal when no exact build", equal, "darwin", "riscv64", "0.2.0", 0, false, "uni"},
		{"empty arch only matches universal", equal, "darwin", "", "0.2.0", 0, false, "uni"},
		{"invalid current version takes any", mac, "windows", "amd64", "dev", 0, false, "w035"},
		{"prerelease ordering", pre, "windows", "amd64", "0.3.0-dev.2", 0, false, "rc2"},
		{"release is newer than its prereleases", pre, "windows", "amd64", "0.3.0", 0, true, ""},
		{"rollout 0 needs explicit", []store.Release{rel("z", "1.0.0", "darwin", "arm64", 0, true)}, "darwin", "arm64", "0.1.0", 0, false, ""},
		{"rollout 0 explicit", []store.Release{rel("z", "1.0.0", "darwin", "arm64", 0, true)}, "darwin", "arm64", "0.1.0", 0, true, "z"},
		{"rollout 100 includes bucket 99", []store.Release{rel("h", "1.0.0", "darwin", "arm64", 100, true)}, "darwin", "arm64", "0.1.0", 99, false, "h"},
		{"no releases", nil, "darwin", "arm64", "0.1.0", 0, true, ""},
	}
	for _, c := range cases {
		got := ""
		if r := pickRelease(c.rels, c.os, c.arch, c.current, c.bucket, c.explicit); r != nil {
			got = r.ID
		}
		if got != c.want {
			t.Errorf("%s: pickRelease = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestApplyLatest(t *testing.T) {
	pub := &time.Time{}
	rels := []store.Release{
		{ID: "a", Version: "0.3.0", OS: "darwin", Arch: "universal", RolloutPct: 0, PublishedAt: pub},
		{ID: "b", Version: "0.4.0", OS: "darwin", Arch: "universal", RolloutPct: 0},
	}
	v := "0.3.0"
	cases := []struct {
		d    store.Device
		want store.Device
	}{
		{store.Device{OS: "darwin", Arch: "arm64", AgentVersion: "0.2.0"},
			store.Device{OS: "darwin", Arch: "arm64", AgentVersion: "0.2.0", LatestVersion: &v, UpdateAvailable: true}},
		{store.Device{OS: "darwin", Arch: "arm64", AgentVersion: "0.3.0"},
			store.Device{OS: "darwin", Arch: "arm64", AgentVersion: "0.3.0", LatestVersion: &v}},
		{store.Device{OS: "darwin", Arch: "arm64", AgentVersion: "0.3.1-dev.4"},
			store.Device{OS: "darwin", Arch: "arm64", AgentVersion: "0.3.1-dev.4", LatestVersion: &v}},
		{store.Device{OS: "linux", Arch: "amd64", AgentVersion: "0.1.0"},
			store.Device{OS: "linux", Arch: "amd64", AgentVersion: "0.1.0"}},
	}
	for _, c := range cases {
		d := c.d
		applyLatest(&d, rels)
		if !reflect.DeepEqual(d, c.want) {
			t.Errorf("applyLatest(%+v) = %+v, want %+v", c.d, d, c.want)
		}
	}
}

func TestDownloadOS(t *testing.T) {
	for in, want := range map[string]string{"darwin": "macos", "windows": "windows", "linux": ""} {
		if got := downloadOS(in); got != want {
			t.Errorf("downloadOS(%q) = %q, want %q", in, got, want)
		}
	}
}
