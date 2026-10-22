package sysinfo

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ruolez/remotectl/proto"
)

// Every call here exists on Windows 7 and is resolved lazily, so the import
// table stays within what the Windows 7 build allows (check-imports.sh).
// Console user, idle time and the lock screen are only right from a process
// in the console session: the helper, not the service.
var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	wtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")

	procGetTickCount64          = kernel32.NewProc("GetTickCount64")
	procGetTickCount            = kernel32.NewProc("GetTickCount")
	procGlobalMemoryStatusEx    = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes          = kernel32.NewProc("GetSystemTimes")
	procGetLastInputInfo        = user32.NewProc("GetLastInputInfo")
	procOpenInputDesktop        = user32.NewProc("OpenInputDesktop")
	procCloseDesktop            = user32.NewProc("CloseDesktop")
	procGetUserObjectInfo       = user32.NewProc("GetUserObjectInformationW")
	procWTSQuerySessionInfo     = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory           = wtsapi32.NewProc("WTSFreeMemory")
	procWTSGetActiveConsoleSess = kernel32.NewProc("WTSGetActiveConsoleSessionId")
)

func osInfo() (name, version, build string) {
	v := windows.RtlGetVersion()
	name, version = "Windows", fmt.Sprintf("%d.%d", v.MajorVersion, v.MinorVersion)
	build = fmt.Sprint(v.BuildNumber)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return name, version, build
	}
	defer k.Close()
	if p, _, err := k.GetStringValue("ProductName"); err == nil && p != "" {
		name = windowsName(p, int(v.BuildNumber))
	}
	if d, _, err := k.GetStringValue("DisplayVersion"); err == nil && d != "" {
		version = d
	} else if r, _, err := k.GetStringValue("ReleaseId"); err == nil && r != "" {
		version = r
	} else if c, _, err := k.GetStringValue("CSDVersion"); err == nil && c != "" {
		version = c // "Service Pack 1" on Windows 7
	}
	if ubr, _, err := k.GetIntegerValue("UBR"); err == nil {
		build = fmt.Sprintf("%d.%d", v.BuildNumber, ubr)
	}
	return name, version, build
}

func uptime() int64 {
	ms, _, _ := procGetTickCount64.Call()
	return int64(ms / 1000)
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memory() (total, used uint64) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, 0
	}
	return m.TotalPhys, m.TotalPhys - min(m.AvailPhys, m.TotalPhys)
}

func ft(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

func readCPU() (cpuTimes, bool) {
	var idle, kernel, user windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return cpuTimes{}, false
	}
	// Kernel time includes idle time.
	return cpuTimes{idle: ft(idle), total: ft(kernel) + ft(user)}, true
}

func idleSeconds() *int64 {
	info := struct{ Size, Time uint32 }{Size: 8}
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return nil
	}
	now, _, _ := procGetTickCount.Call()
	s := int64(uint32(now)-info.Time) / 1000 // 32-bit tick arithmetic wraps correctly
	return &s
}

const (
	wtsUserName   = 5
	wtsDomainName = 7
)

func wtsString(session uint32, class int) (string, bool) {
	var buf *uint16
	var n uint32
	r, _, _ := procWTSQuerySessionInfo.Call(0, uintptr(session), uintptr(class), uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 || buf == nil {
		return "", false
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), true
}

// consoleUser is the user signed in at the physical console, "" for nobody.
func consoleUser() *string {
	sess, _, _ := procWTSGetActiveConsoleSess.Call()
	if uint32(sess) == 0xFFFFFFFF {
		return nil
	}
	u, ok := wtsString(uint32(sess), wtsUserName)
	if !ok {
		return nil
	}
	if d, ok := wtsString(uint32(sess), wtsDomainName); ok && d != "" && u != "" && !strings.EqualFold(d, hostname()) {
		u = d + `\` + u
	}
	return &u
}

func hostname() string {
	n, _ := windows.ComputerName()
	return n
}

const uoiName = 2

// InputDesktopLocked reports whether the input desktop is not the user's
// ("Winlogon" while locked or at a UAC prompt, "Screen-saver").
func InputDesktopLocked() bool {
	h, _, _ := procOpenInputDesktop.Call(0, 0, 0x0001 /* DESKTOP_READOBJECTS */)
	if h == 0 {
		return true // no access to it: the secure desktop
	}
	defer procCloseDesktop.Call(h)
	var name [64]uint16
	var need uint32
	r, _, _ := procGetUserObjectInfo.Call(h, uoiName, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return false
	}
	return !strings.EqualFold(windows.UTF16ToString(name[:]), "Default")
}

// disks lists fixed drives with a letter (volumes mounted without a letter
// are not listed).
func disks() []proto.DiskInfo {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []proto.DiskInfo
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(p) != windows.DRIVE_FIXED {
			continue
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil || total == 0 {
			continue
		}
		var label, fs [261]uint16
		_ = windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, &fs[0], uint32(len(fs)))
		name := windows.UTF16ToString(label[:])
		if name == "" {
			name = "Local Disk"
		}
		out = append(out, proto.DiskInfo{Name: name + " (" + root[:2] + ")", Mount: root, FS: windows.UTF16ToString(fs[:]),
			Total: total, Free: free})
	}
	return out
}
