package files

import (
	"io/fs"
	"os"
)

// linkRename is the no-replace fallback for file systems without an atomic
// exclusive rename: a hard link fails if the target exists. Where hard links
// are unsupported too (FAT, some network shares) it degrades to a checked
// rename, which can race but never knowingly replaces a file.
func linkRename(from, to string) error {
	err := os.Link(from, to)
	if err == nil {
		return os.Remove(from)
	}
	if os.IsExist(err) {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: fs.ErrExist}
	}
	return os.Rename(from, to)
}
