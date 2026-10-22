package sysinfo

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

func TestDue(t *testing.T) {
	alice, bob := "alice", "bob"
	yes, no := true, false
	last := &proto.HostInfo{User: &alice, Locked: &no}
	cases := []struct {
		name   string
		last   *proto.HostInfo
		cur    proto.HostInfo
		since  time.Duration
		kicked bool
		want   bool
	}{
		{"first reading", nil, proto.HostInfo{}, 0, false, true},
		{"kicked by a new connection", last, *last, time.Second, true, true},
		{"nothing changed yet", last, *last, 30 * time.Second, false, false},
		{"the minute is up", last, *last, reportEvery, false, true},
		{"locked, gap passed", last, proto.HostInfo{User: &alice, Locked: &yes}, changeGap, false, true},
		{"locked, too soon", last, proto.HostInfo{User: &alice, Locked: &yes}, changeGap - time.Second, false, false},
		{"other user", last, proto.HostInfo{User: &bob, Locked: &no}, changeGap, false, true},
		{"user signed out", last, proto.HostInfo{Locked: &no}, changeGap, false, true},
	}
	for _, c := range cases {
		if got := due(c.last, c.cur, c.since, c.kicked); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReporterSendsOnKickOnlyWhenEnabled(t *testing.T) {
	var enabled atomic.Bool
	sent := make(chan proto.HostInfo, 4)
	r := NewReporter(func() proto.HostInfo { return proto.HostInfo{OSName: "test"} },
		func(h proto.HostInfo) error { sent <- h; return nil }, enabled.Load)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	r.Kick()
	select {
	case <-sent:
		t.Fatal("sent while the server does not accept device.info")
	case <-time.After(100 * time.Millisecond):
	}
	enabled.Store(true)
	r.Kick()
	select {
	case h := <-sent:
		if h.OSName != "test" {
			t.Fatalf("sent %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no report after kick")
	}
}
