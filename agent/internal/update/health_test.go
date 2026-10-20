package update

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestDecideHealth(t *testing.T) {
	marker := Marker{From: "0.3.0", To: "0.3.1", AppliedAt: t0, Starts: 1}
	with := func(f func(*Marker)) Marker { m := marker; f(&m); return m }
	up := func(in HealthInput) HealthInput { in.ServerUp, in.UserPresent = true, true; return in }
	cases := []struct {
		name string
		in   HealthInput
		want HealthAction
	}{
		{"acked new build", HealthInput{Marker: marker, Running: "0.3.1", Acked: true, Now: t0.Add(time.Minute)}, HealthHealthy},
		{"waiting within the window", up(HealthInput{Marker: marker, Running: "0.3.1", Since: 4 * time.Minute, Now: t0.Add(4 * time.Minute)}), HealthWait},
		{"no ack for 5 min with server and user", up(HealthInput{Marker: marker, Running: "0.3.1", Since: HealthWindow, Now: t0.Add(HealthWindow)}), HealthRollback},
		{"server down never rolls back", HealthInput{Marker: marker, Running: "0.3.1", UserPresent: true, Since: time.Hour, Now: t0.Add(time.Hour)}, HealthWait},
		{"no user present waits", HealthInput{Marker: marker, Running: "0.3.1", ServerUp: true, Since: time.Hour, Now: t0.Add(time.Hour)}, HealthWait},
		{"crash loop with server up", HealthInput{Marker: with(func(m *Marker) { m.Starts = MaxStarts }), Running: "0.3.1", ServerUp: true, Now: t0.Add(time.Minute)}, HealthRollback},
		{"crash loop with server down", HealthInput{Marker: with(func(m *Marker) { m.Starts = 9 }), Running: "0.3.1", UserPresent: true, Now: t0.Add(time.Minute)}, HealthWait},
		{"three starts is not a loop", HealthInput{Marker: with(func(m *Marker) { m.Starts = MaxStarts - 1 }), Running: "0.3.1", ServerUp: true, Now: t0.Add(time.Minute)}, HealthWait},
		{"install never took", up(HealthInput{Marker: marker, Running: "0.3.0", Now: t0.Add(time.Minute)}), HealthReportFailed},
		{"restored after rollback", HealthInput{Marker: with(func(m *Marker) { m.RolledBack = true }), Running: "0.3.0", Now: t0.Add(time.Minute)}, HealthReportRolledBack},
		{"rollback already started", up(HealthInput{Marker: with(func(m *Marker) { m.RolledBack = true }), Running: "0.3.1", Since: time.Hour, Now: t0.Add(time.Hour)}), HealthWait},
		{"rolled back build that acks keeps running", HealthInput{Marker: with(func(m *Marker) { m.RolledBack = true }), Running: "0.3.1", Acked: true, Now: t0.Add(time.Hour)}, HealthHealthy},
		{"some other version", up(HealthInput{Marker: marker, Running: "0.4.0", Now: t0.Add(time.Minute)}), HealthAbandon},
		{"older than a day", up(HealthInput{Marker: marker, Running: "0.3.1", Since: time.Minute, Now: t0.Add(AbandonAfter)}), HealthAbandon},
		{"old failure still reported", HealthInput{Marker: marker, Running: "0.3.0", Now: t0.Add(48 * time.Hour)}, HealthReportFailed},
	}
	for _, c := range cases {
		if got := DecideHealth(c.in); got != c.want {
			t.Errorf("%s: DecideHealth = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-pending.json")
	if _, err := ReadMarker(path); err == nil {
		t.Fatal("missing marker read without error")
	}
	want := Marker{ID: "r1", From: "0.3.0", To: "0.3.1", Previous: `C:\x\installed.msi`, AppliedAt: t0, Starts: 2,
		WatchFrom: t0.Add(time.Minute), RolledBack: true, Error: "no hello_ack"}
	if err := WriteMarker(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMarker(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadMarker = %+v, %v; want %+v", got, err, want)
	}
}

func TestSkipList(t *testing.T) {
	dir := t.TempDir()
	if got := LoadSkip(dir); len(got) != 0 {
		t.Fatalf("empty dir skip list = %v", got)
	}
	for _, v := range []string{"0.3.1", "0.3.2", "0.3.1"} {
		if err := AddSkip(dir, v); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := LoadSkip(dir), map[string]bool{"0.3.1": true, "0.3.2": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skip list = %v, want %v", got, want)
	}
}
