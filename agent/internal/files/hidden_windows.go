package files

import (
	"io/fs"
	"syscall"
)

// hiddenAttr reports FILE_ATTRIBUTE_HIDDEN (or SYSTEM, which Explorer also hides).
func hiddenAttr(info fs.FileInfo) bool {
	a, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && a.FileAttributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
}
