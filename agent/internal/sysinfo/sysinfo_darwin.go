package sysinfo

/*
#cgo LDFLAGS: -framework CoreFoundation -framework CoreGraphics -framework SystemConfiguration
#include <stdlib.h>
#include <mach/mach.h>
#include <mach/mach_host.h>
#include <CoreGraphics/CoreGraphics.h>
#include <SystemConfiguration/SystemConfiguration.h>

// Memory "used" as Activity Monitor counts it: app memory (anonymous pages
// minus purgeable) + wired + compressed.
static int rc_mem_used(unsigned long long *used) {
	vm_statistics64_data_t vm;
	mach_msg_type_number_t n = HOST_VM_INFO64_COUNT;
	if (host_statistics64(mach_host_self(), HOST_VM_INFO64, (host_info64_t)&vm, &n) != KERN_SUCCESS) return -1;
	vm_size_t page = 0;
	host_page_size(mach_host_self(), &page);
	unsigned long long app = vm.internal_page_count > vm.purgeable_count ? vm.internal_page_count - vm.purgeable_count : 0;
	*used = (app + vm.wire_count + vm.compressor_page_count) * (unsigned long long)page;
	return 0;
}

// This process's memory as Activity Monitor shows it.
static unsigned long long rc_self_footprint(void) {
	task_vm_info_data_t info;
	mach_msg_type_number_t n = TASK_VM_INFO_COUNT;
	if (task_info(mach_task_self(), TASK_VM_INFO, (task_info_t)&info, &n) != KERN_SUCCESS) return 0;
	return info.phys_footprint;
}

static int rc_cpu_ticks(unsigned long long *idle, unsigned long long *total) {
	host_cpu_load_info_data_t c;
	mach_msg_type_number_t n = HOST_CPU_LOAD_INFO_COUNT;
	if (host_statistics(mach_host_self(), HOST_CPU_LOAD_INFO, (host_info_t)&c, &n) != KERN_SUCCESS) return -1;
	*idle = c.cpu_ticks[CPU_STATE_IDLE];
	*total = (unsigned long long)c.cpu_ticks[CPU_STATE_USER] + c.cpu_ticks[CPU_STATE_SYSTEM] +
	         c.cpu_ticks[CPU_STATE_IDLE] + c.cpu_ticks[CPU_STATE_NICE];
	return 0;
}

static double rc_idle_seconds(void) {
	return CGEventSourceSecondsSinceLastEventType(kCGEventSourceStateCombinedSessionState, kCGAnyInputEventType);
}

// The user at the console as a malloc'd UTF-8 string; "" at the login window.
static char *rc_console_user(void) {
	uid_t uid;
	CFStringRef name = SCDynamicStoreCopyConsoleUser(NULL, &uid, NULL);
	if (!name) return NULL;
	char buf[256] = {0};
	Boolean ok = CFStringGetCString(name, buf, sizeof buf, kCFStringEncodingUTF8);
	CFRelease(name);
	if (!ok) return NULL;
	if (strcmp(buf, "loginwindow") == 0) buf[0] = 0;
	return strdup(buf);
}
*/
import "C"

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ruolez/remotectl/proto"
)

func osInfo() (name, version, build string) {
	version, _ = unix.Sysctl("kern.osproductversion")
	build, _ = unix.Sysctl("kern.osversion")
	return "macOS", version, build
}

func uptime() int64 {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return 0
	}
	var now unix.Timeval
	if unix.Gettimeofday(&now) != nil {
		return 0
	}
	return max(now.Sec-tv.Sec, 0)
}

func memory() (total, used uint64) {
	total, _ = unix.SysctlUint64("hw.memsize")
	var u C.ulonglong
	if C.rc_mem_used(&u) == 0 {
		used = min(uint64(u), total)
	}
	return total, used
}

// SelfMemory is this process's physical footprint in bytes.
func SelfMemory() uint64 { return uint64(C.rc_self_footprint()) }

func readCPU() (cpuTimes, bool) {
	var idle, total C.ulonglong
	if C.rc_cpu_ticks(&idle, &total) != 0 {
		return cpuTimes{}, false
	}
	return cpuTimes{idle: uint64(idle), total: uint64(total)}, true
}

func idleSeconds() *int64 {
	s := int64(C.rc_idle_seconds())
	if s < 0 {
		return nil
	}
	return &s
}

func consoleUser() *string {
	c := C.rc_console_user()
	if c == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(c))
	u := C.GoString(c)
	return &u
}

// disks lists the startup volume and the local volumes under /Volumes (the
// system's own APFS volumes under /System/Volumes share the startup
// volume's space and are left out).
func disks() []proto.DiskInfo {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return nil
	}
	buf := make([]unix.Statfs_t, n)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil
	}
	var out []proto.DiskInfo
	for _, s := range buf[:n] {
		point := unix.ByteSliceToString(s.Mntonname[:])
		if s.Flags&unix.MNT_LOCAL == 0 || s.Blocks == 0 || (point != "/" && !strings.HasPrefix(point, "/Volumes/")) {
			continue
		}
		name := ""
		if point != "/" {
			name = strings.TrimPrefix(point, "/Volumes/")
		}
		bs := uint64(s.Bsize)
		out = append(out, proto.DiskInfo{Name: name, Mount: point, FS: unix.ByteSliceToString(s.Fstypename[:]),
			Total: s.Blocks * bs, Free: s.Bavail * bs})
	}
	return out
}
