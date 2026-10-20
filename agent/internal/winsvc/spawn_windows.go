package winsvc

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const unsafeSizeofSA = unsafe.Sizeof(windows.SecurityAttributes{})

// noConsoleSession is what WTSGetActiveConsoleSessionId returns while the
// console is being attached/detached.
const noConsoleSession = 0xFFFFFFFF

var errNoConsole = errors.New("winsvc: no active console session")

func activeConsoleSession() (uint32, bool) {
	id := windows.WTSGetActiveConsoleSessionId()
	return id, id != noConsoleSession
}

// spawnInSession starts exe with args as SYSTEM inside Windows session sid on
// winsta0\default, so it can capture the console, inject input and follow
// desktop switches (UAC, lock screen). Needs SeTcbPrivilege (LocalSystem).
func spawnInSession(exe string, args []string, sid uint32) (windows.Handle, uint32, error) {
	var self windows.Token
	access := uint32(windows.TOKEN_DUPLICATE | windows.TOKEN_QUERY | windows.TOKEN_ASSIGN_PRIMARY |
		windows.TOKEN_ADJUST_DEFAULT | windows.TOKEN_ADJUST_SESSIONID)
	if err := windows.OpenProcessToken(windows.CurrentProcess(), access, &self); err != nil {
		return 0, 0, err
	}
	defer self.Close()

	var tok windows.Token
	if err := windows.DuplicateTokenEx(self, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation,
		windows.TokenPrimary, &tok); err != nil {
		return 0, 0, err
	}
	defer tok.Close()
	if err := windows.SetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&sid)),
		uint32(unsafe.Sizeof(sid))); err != nil {
		return 0, 0, err
	}

	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, tok, false); err != nil {
		env = nil
	} else {
		defer windows.DestroyEnvironmentBlock(env)
	}

	exe16, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, 0, err
	}
	cmd16, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	if err != nil {
		return 0, 0, err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcessAsUser(tok, exe16, cmd16, nil, nil, false, flags, env, nil, &si, &pi); err != nil {
		return 0, 0, err
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, pi.ProcessId, nil
}

// waitProcess returns a channel closed when the process exits.
func waitProcess(h windows.Handle) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		windows.WaitForSingleObject(h, windows.INFINITE)
		close(ch)
	}()
	return ch
}
