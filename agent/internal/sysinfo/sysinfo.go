// Package sysinfo collects what the dashboard shows about a device (PROTOCOL
// §3 device.info): OS version, console user, idle time, uptime, CPU, memory
// and disks. Each platform file supplies the raw readings; the parsers and
// the arithmetic here are shared and tested on every host.
package sysinfo

import (
	"bufio"
	"io"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// cpuTimes are cumulative CPU counters in any consistent unit.
type cpuTimes struct{ idle, total uint64 }

// minCPUWindow is the shortest interval CPU use is measured over; a shorter
// one (a report right after the previous reading) repeats the last value.
const minCPUWindow = time.Second

// Collector reads the host. CPU is the share busy since the previous
// reading at least minCPUWindow ago.
type Collector struct {
	// Locked reports the lock screen; nil when the platform has no notion of it.
	Locked func() bool
	// VirtualDisplay reports the virtual display (Windows); nil: none.
	VirtualDisplay func() *proto.VirtualDisplayInfo
	// CaptureMemory is the capture worker's memory; nil or 0: none runs.
	CaptureMemory func() uint64

	mu      sync.Mutex
	prev    *cpuTimes
	prevAt  time.Time
	lastPct *float64
}

func (c *Collector) Collect() proto.HostInfo {
	name, version, build := osInfo()
	info := proto.HostInfo{OSName: name, OSVersion: version, OSBuild: build, UptimeS: uptime(),
		CPUCount: runtime.NumCPU(), User: consoleUser(), IdleS: idleSeconds(), Disks: disks()}
	info.MemTotal, info.MemUsed = memory()
	if c.Locked != nil {
		l := c.Locked()
		info.Locked = &l
	}
	if c.VirtualDisplay != nil {
		info.VirtualDisplay = c.VirtualDisplay()
	}
	info.Agent = &proto.AgentMemory{Main: SelfMemory(), GoHeap: goHeap()}
	if c.CaptureMemory != nil {
		info.Agent.Capture = c.CaptureMemory()
	}
	if cur, ok := readCPU(); ok {
		c.mu.Lock()
		if c.prev == nil || time.Since(c.prevAt) >= minCPUWindow {
			c.lastPct = cpuPct(c.prev, cur)
			c.prev, c.prevAt = &cur, time.Now()
		}
		info.CPUPct = c.lastPct
		c.mu.Unlock()
	}
	info.Disks = distinctDisks(info.Disks)
	return info
}

// distinctDisks drops a volume that reports exactly the same size and free
// space as one before it: the same disk mounted twice (a container's root
// and its data volume).
func distinctDisks(ds []proto.DiskInfo) []proto.DiskInfo {
	out := []proto.DiskInfo{}
	for _, d := range ds {
		dup := false
		for _, o := range out {
			dup = dup || (o.Total == d.Total && o.Free == d.Free)
		}
		if !dup {
			out = append(out, d)
		}
	}
	return out
}

// cpuPct is the busy share between two readings, or nil without a previous
// one (or when the counters went backwards).
func cpuPct(prev *cpuTimes, cur cpuTimes) *float64 {
	if prev == nil || cur.total <= prev.total || cur.idle < prev.idle {
		return nil
	}
	dt := float64(cur.total - prev.total)
	busy := dt - float64(cur.idle-prev.idle)
	p := max(0, min(100, busy/dt*100))
	return &p
}

// parseMeminfo reads /proc/meminfo: used = MemTotal − MemAvailable.
func parseMeminfo(r io.Reader) (total, used uint64) {
	var avail uint64
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	if avail > total {
		avail = total
	}
	return total, total - avail
}

// parseProcStat reads the aggregate "cpu" line of /proc/stat.
// goHeap is the bytes of live and not yet swept Go objects.
func goHeap() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// parseStatusRSS reads VmRSS from /proc/<pid>/status.
func parseStatusRSS(r io.Reader) uint64 {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "VmRSS:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb * 1024
		}
	}
	return 0
}

func parseProcStat(r io.Reader) (cpuTimes, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var t cpuTimes
		for i, v := range f[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			// user nice system idle iowait irq softirq steal (guest is in user already)
			if i >= 8 {
				break
			}
			t.total += n
			if i == 3 || i == 4 {
				t.idle += n
			}
		}
		return t, true
	}
	return cpuTimes{}, false
}

type mount struct{ source, point, fs string }

// realFS are file systems that store data on a disk.
var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "overlay": true}

// parseMounts reads /proc/self/mounts, keeping disk-backed mounts outside
// the kernel's own trees, one per source device.
func parseMounts(r io.Reader) []mount {
	var out []mount
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !realFS[f[2]] {
			continue
		}
		point := unescapeMount(f[1])
		if strings.HasPrefix(point, "/proc") || strings.HasPrefix(point, "/sys") ||
			strings.HasPrefix(point, "/dev") || strings.HasPrefix(point, "/run") {
			continue
		}
		if seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		out = append(out, mount{source: f[0], point: point, fs: f[2]})
	}
	return out
}

// unescapeMount undoes the octal escapes /proc/mounts uses for spaces etc.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseOSRelease reads /etc/os-release: NAME and VERSION_ID.
func parseOSRelease(r io.Reader) (name, version string) {
	sc := bufio.NewScanner(r)
	pretty := ""
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "NAME":
			name = v
		case "PRETTY_NAME":
			pretty = v
		case "VERSION_ID":
			version = v
		}
	}
	if name == "" {
		name = pretty
	}
	return name, version
}

// windowsName fixes ProductName, which says "Windows 10" on Windows 11
// (build 22000 and later).
func windowsName(product string, build int) string {
	if build >= 22000 {
		return strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	return product
}
