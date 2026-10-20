package files

import (
	"os"

	"golang.org/x/sys/windows"
)

// renameNoReplace renames from to to, failing with an fs.ErrExist error if
// to exists (MoveFileEx without MOVEFILE_REPLACE_EXISTING).
func renameNoReplace(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(f, t, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
