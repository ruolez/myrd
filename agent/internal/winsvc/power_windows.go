//go:build windows

package winsvc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// restartDelay is how long Windows shows its restart notice before it
// restarts; the session ends meanwhile.
const restartDelay = 5 * time.Second

// RestartDevice schedules a restart of Windows in restartDelay. Applications
// are closed without asking: nobody may be at the device to answer them. The
// helper and the service run as SYSTEM, which holds SeShutdownPrivilege.
func RestartDevice() error {
	if err := enablePrivilege("SeShutdownPrivilege"); err != nil {
		return fmt.Errorf("enable SeShutdownPrivilege: %w", err)
	}
	msg, _ := windows.UTF16PtrFromString("A remote restart was requested through RemoteAgent.")
	const reason = windows.SHTDN_REASON_MAJOR_OTHER | windows.SHTDN_REASON_MINOR_OTHER | windows.SHTDN_REASON_FLAG_PLANNED
	return windows.InitiateSystemShutdownEx(nil, msg, uint32(restartDelay/time.Second), true, true, reason)
}

func enablePrivilege(name string) error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, n, &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}

// StartServiceRestart runs `remote-agent restart-service` outside this
// process tree and job, so it outlives the service it stops.
func StartServiceRestart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	start := func(flags uint32) error {
		cmd := exec.Command(exe, "restart-service")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	err = start(base | windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		err = start(base) // the job forbids breakaway
	}
	return err
}

// RestartService is `remote-agent restart-service`: after a moment for the
// session to end, it stops the RemoteAgent service, waits for it to stop and
// starts it again.
func RestartService() error {
	time.Sleep(time.Second)
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("the service did not stop within 30 s")
		}
	}
	return s.Start()
}
