//go:build cgo

package native

import "github.com/ruolez/remotectl/agent/internal/sysinfo"

// screenLocked: Windows reports its lock screen from Go (the native library
// has no rc_screen_locked); the session helper runs in the console session.
func screenLocked() bool { return sysinfo.LockScreenShown() }
