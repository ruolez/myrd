package sysinfo

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ruolez/remotectl/proto"
)

func osInfo() (name, version, build string) {
	if f, err := os.Open("/etc/os-release"); err == nil {
		defer f.Close()
		name, version = parseOSRelease(f)
	}
	if name == "" {
		name = "Linux"
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		build = unix.ByteSliceToString(u.Release[:])
	}
	return name, version, build
}

func uptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	s, _ := strconv.ParseFloat(f[0], 64)
	return int64(s)
}

func memory() (total, used uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	return parseMeminfo(f)
}

// SelfMemory is this process's resident set in bytes.
func SelfMemory() uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	return parseStatusRSS(f)
}

func readCPU() (cpuTimes, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	defer f.Close()
	return parseProcStat(f)
}

func disks() []proto.DiskInfo {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []proto.DiskInfo
	for _, m := range parseMounts(f) {
		if st, err := os.Stat(m.point); err != nil || !st.IsDir() {
			continue // a bind-mounted file (/etc/hosts in a container)
		}
		var s unix.Statfs_t
		if unix.Statfs(m.point, &s) != nil || s.Blocks == 0 {
			continue
		}
		bs := uint64(s.Bsize)
		out = append(out, proto.DiskInfo{Mount: m.point, FS: m.fs, Total: s.Blocks * bs, Free: s.Bavail * bs})
	}
	return out
}

// The Linux agent is the Docker demo device: no login session to ask about.
func consoleUser() *string { return nil }
func idleSeconds() *int64  { return nil }
