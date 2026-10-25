//go:build !windows

package capproc

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return nil }
