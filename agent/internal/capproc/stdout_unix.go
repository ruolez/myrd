//go:build !windows

package capproc

import (
	"os"

	"golang.org/x/sys/unix"
)

// TakeStdout returns the worker's stdout for the wire and points fd 1 at
// stderr, so nothing a native library prints can land in the frame stream.
func TakeStdout() (*os.File, error) {
	fd, err := unix.Dup(1)
	if err != nil {
		return nil, err
	}
	if err := unix.Dup2(2, 1); err != nil {
		unix.Close(fd)
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "capture-wire"), nil
}
