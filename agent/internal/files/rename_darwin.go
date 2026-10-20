package files

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames from to to, failing with an fs.ErrExist error if
// to exists.
func renameNoReplace(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return linkRename(from, to)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
