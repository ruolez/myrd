//go:build !windows

package files

import (
	"os"
	"syscall"
)

// openRead opens without blocking on FIFOs and devices; the caller rejects
// anything that is not a regular file.
func openRead(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
