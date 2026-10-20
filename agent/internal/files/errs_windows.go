package files

import (
	"errors"

	"golang.org/x/sys/windows"
)

func platformCode(err error) string {
	switch {
	case errors.Is(err, windows.ERROR_DIRECTORY):
		return CodeNotDir
	case errors.Is(err, windows.ERROR_DISK_FULL), errors.Is(err, windows.ERROR_HANDLE_DISK_FULL):
		return CodeNoSpace
	case errors.Is(err, windows.ERROR_INVALID_NAME), errors.Is(err, windows.ERROR_FILENAME_EXCED_RANGE):
		return CodeBadName
	case errors.Is(err, windows.ERROR_PATH_NOT_FOUND), errors.Is(err, windows.ERROR_BAD_NETPATH):
		return CodeNotFound
	}
	return ""
}
