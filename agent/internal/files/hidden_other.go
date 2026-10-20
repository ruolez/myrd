//go:build !darwin && !windows

package files

import "io/fs"

func hiddenAttr(fs.FileInfo) bool { return false }
