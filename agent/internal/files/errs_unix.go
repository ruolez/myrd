//go:build !windows

package files

import (
	"errors"
	"runtime"
	"syscall"
)

func platformCode(err error) string {
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return CodeNotDir
	case errors.Is(err, syscall.EISDIR):
		return CodeNotFile
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return CodeNoSpace
	case errors.Is(err, syscall.ENAMETOOLONG):
		return CodeBadName
	case errors.Is(err, syscall.EPERM) && runtime.GOOS == "darwin":
		// TCC privacy denials surface as EPERM; plain permission bits give EACCES.
		return CodeOSDenied
	}
	return ""
}
