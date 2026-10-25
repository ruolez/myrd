package sysinfo

import (
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func TestCPUPct(t *testing.T) {
	prev := &cpuTimes{idle: 900, total: 1000}
	cases := []struct {
		name string
		prev *cpuTimes
		cur  cpuTimes
		want float64
		none bool
	}{
		{"quarter busy", prev, cpuTimes{idle: 1050, total: 1200}, 25, false},
		{"idle", prev, cpuTimes{idle: 1000, total: 1100}, 0, false},
		{"no previous reading", nil, cpuTimes{idle: 1, total: 2}, 0, true},
		{"counters reset", prev, cpuTimes{idle: 10, total: 20}, 0, true},
	}
	for _, c := range cases {
		got := cpuPct(c.prev, c.cur)
		if (got == nil) != c.none || (got != nil && *got != c.want) {
			t.Errorf("%s: got %v, want %v (none %v)", c.name, got, c.want, c.none)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	total, used := parseMeminfo(strings.NewReader("MemTotal:       16384000 kB\nMemFree:  100 kB\nMemAvailable:    4096000 kB\n"))
	if total != 16384000*1024 || used != (16384000-4096000)*1024 {
		t.Fatalf("total %d used %d", total, used)
	}
	if total, used := parseMeminfo(strings.NewReader("garbage\n")); total != 0 || used != 0 {
		t.Fatalf("garbage: %d %d", total, used)
	}
}

func TestParseProcStat(t *testing.T) {
	got, ok := parseProcStat(strings.NewReader("cpu  100 5 50 800 20 1 2 3 4 5\ncpu0 1 1 1 1 1 1 1 1\n"))
	want := cpuTimes{idle: 820, total: 100 + 5 + 50 + 800 + 20 + 1 + 2 + 3}
	if !ok || got != want {
		t.Fatalf("got %+v %v, want %+v", got, ok, want)
	}
	if _, ok := parseProcStat(strings.NewReader("intr 1 2 3\n")); ok {
		t.Fatal("no cpu line should fail")
	}
}

func TestParseMounts(t *testing.T) {
	in := `overlay / overlay rw,relatime 0 0
proc /proc proc rw 0 0
/dev/vda1 /data ext4 rw 0 0
/dev/vda1 /etc/hosts ext4 rw 0 0
tmpfs /dev/shm tmpfs rw 0 0
/dev/sdb1 /mnt/My\040Disk vfat rw 0 0
/dev/sdc1 /run/media/x ext4 rw 0 0
`
	got := parseMounts(strings.NewReader(in))
	want := []mount{{"overlay", "/", "overlay"}, {"/dev/vda1", "/data", "ext4"}, {"/dev/sdb1", "/mnt/My Disk", "vfat"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mount %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	name, ver := parseOSRelease(strings.NewReader("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\n"))
	if name != "Debian GNU/Linux" || ver != "12" {
		t.Fatalf("%q %q", name, ver)
	}
	if name, _ := parseOSRelease(strings.NewReader("PRETTY_NAME=Alpine\n")); name != "Alpine" {
		t.Fatalf("pretty fallback: %q", name)
	}
}

func TestWindowsName(t *testing.T) {
	cases := map[int]string{19045: "Windows 10 Pro", 22631: "Windows 11 Pro", 26100: "Windows 11 Pro"}
	for build, want := range cases {
		if got := windowsName("Windows 10 Pro", build); got != want {
			t.Errorf("build %d: %q, want %q", build, got, want)
		}
	}
}

func TestCollectFillsCommonFields(t *testing.T) {
	c := &Collector{Locked: func() bool { return true }}
	c.Collect()
	info := c.Collect()
	if info.CPUCount <= 0 || info.Locked == nil || !*info.Locked || info.Disks == nil {
		t.Fatalf("collect: %+v", info)
	}
}

func TestParseStatusRSS(t *testing.T) {
	status := "Name:\tremote-agent\nVmHWM:\t   90280 kB\nVmRSS:\t   37344 kB\nRssAnon:\t   21104 kB\n"
	if got, want := parseStatusRSS(strings.NewReader(status)), uint64(37344*1024); got != want {
		t.Fatalf("rss %d, want %d", got, want)
	}
	if got := parseStatusRSS(strings.NewReader("VmRSS: lots\n")); got != 0 {
		t.Fatalf("unparsable rss read as %d", got)
	}
}

func TestCollectReportsAgentMemory(t *testing.T) {
	const capture = 210 << 20
	c := &Collector{CaptureMemory: func() uint64 { return capture }}
	a := c.Collect().Agent
	if a == nil || a.Main == 0 || a.GoHeap == 0 || a.Capture != capture || a.Service != 0 {
		t.Fatalf("agent memory %+v, want this process measured and the capture worker's %d", a, capture)
	}
	if a.GoHeap > a.Main {
		t.Fatalf("Go heap %d above the process total %d", a.GoHeap, a.Main)
	}
}

func TestDistinctDisks(t *testing.T) {
	in := []proto.DiskInfo{{Mount: "/", Total: 10, Free: 4}, {Mount: "/data", Total: 10, Free: 4}, {Mount: "/mnt", Total: 10, Free: 5}}
	got := distinctDisks(in)
	if len(got) != 2 || got[0].Mount != "/" || got[1].Mount != "/mnt" {
		t.Fatalf("got %+v", got)
	}
	if got := distinctDisks(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil in: %+v", got)
	}
}
