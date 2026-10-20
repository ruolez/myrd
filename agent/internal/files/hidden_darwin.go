package files

import (
	"io/fs"
	"syscall"
)

// hiddenAttr reports the Finder "hidden" flag (chflags hidden).
func hiddenAttr(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&0x8000 != 0 // UF_HIDDEN
}
