package app

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func TestCleanHostInfo(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	long := strings.Repeat("x", proto.MaxHostInfoText+10)
	idle := int64(-5)
	disks := make([]proto.DiskInfo, proto.MaxHostDisks+3)
	for i := range disks {
		disks[i] = proto.DiskInfo{Mount: "/v", Total: 100, Free: 40}
	}
	disks[0] = proto.DiskInfo{Mount: "/empty"}                       // no size: dropped
	disks[1] = proto.DiskInfo{Mount: "/full", Total: 100, Free: 500} // free capped at total

	out := cleanHostInfo(proto.HostInfo{OSName: long, User: &long, IdleS: &idle, UptimeS: -1,
		CPUPct: ptr(140), CPUCount: -2, MemTotal: 8, MemUsed: 9, Disks: disks})

	if len(out.OSName) != proto.MaxHostInfoText || len(*out.User) != proto.MaxHostInfoText {
		t.Fatalf("text not truncated: %d %d", len(out.OSName), len(*out.User))
	}
	if *out.IdleS != 0 || out.UptimeS != 0 || *out.CPUPct != 100 || out.CPUCount != 0 || out.MemUsed != 8 {
		t.Fatalf("numbers not clamped: idle %d uptime %d cpu %v count %d mem %d", *out.IdleS, out.UptimeS, *out.CPUPct, out.CPUCount, out.MemUsed)
	}
	if len(out.Disks) != proto.MaxHostDisks || out.Disks[0].Mount != "/full" || out.Disks[0].Free != 100 {
		t.Fatalf("disks: %d first %+v", len(out.Disks), out.Disks[0])
	}

	nan := cleanHostInfo(proto.HostInfo{CPUPct: ptr(math.NaN())})
	if nan.CPUPct != nil || nan.User != nil || nan.IdleS != nil || nan.Disks == nil {
		t.Fatalf("absent values: %+v", nan)
	}
	if got := *cleanHostInfo(proto.HostInfo{CPUPct: ptr(12.345)}).CPUPct; got != 12.3 {
		t.Fatalf("cpu rounding: %v", got)
	}
}

func TestCleanHostInfoVirtualDisplay(t *testing.T) {
	long := strings.Repeat("x", proto.MaxHostInfoText+10)
	cases := []struct {
		name string
		in   *proto.VirtualDisplayInfo
		want *proto.VirtualDisplayInfo
	}{
		{"absent", nil, nil},
		{"valid",
			&proto.VirtualDisplayInfo{Supported: true, Headless: true, Driver: proto.VDriverInstalled, Active: true},
			&proto.VirtualDisplayInfo{Supported: true, Headless: true, Driver: proto.VDriverInstalled, Active: true}},
		{"unknown driver state", &proto.VirtualDisplayInfo{Supported: true, Driver: "broken"}, nil},
		{"long error",
			&proto.VirtualDisplayInfo{Supported: true, Driver: proto.VDriverFailed, Error: long},
			&proto.VirtualDisplayInfo{Supported: true, Driver: proto.VDriverFailed, Error: long[:proto.MaxHostInfoText]}},
	}
	for _, c := range cases {
		got := cleanHostInfo(proto.HostInfo{VirtualDisplay: c.in})
		want := proto.HostInfo{Disks: []proto.DiskInfo{}, VirtualDisplay: c.want}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, want)
		}
	}
}

func TestCleanHostInfoAgentMemory(t *testing.T) {
	mem := &proto.AgentMemory{Main: 48 << 20, GoHeap: 9 << 20, Capture: 260 << 20, Service: 15 << 20}
	if got := cleanHostInfo(proto.HostInfo{Agent: mem}).Agent; got == nil || *got != *mem || got == mem {
		t.Fatalf("agent memory %+v, want a copy of %+v", got, mem)
	}
	if got := cleanHostInfo(proto.HostInfo{}).Agent; got != nil {
		t.Fatalf("agent memory %+v from an agent that sent none", got)
	}
}
