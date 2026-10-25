package capproc

import "syscall"

// createNoWindow keeps a console-subsystem child from opening a console.
const createNoWindow = 0x08000000

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
