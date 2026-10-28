package files

import (
	"errors"
	"io/fs"
)

// ErrNoUser is returned by a Host when no user is signed in to act as
// (Windows logon screen).
var ErrNoUser = errors.New("no user signed in")

// errCode maps a file-system error to a protocol error code.
func errCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoUser):
		return CodeNoUser
	case errors.Is(err, ErrBadPath):
		return CodeBadPath
	case errors.Is(err, ErrBadName):
		return CodeBadName
	case errors.Is(err, ErrDenied):
		return CodeDenied
	case errors.Is(err, errNotDir):
		return CodeNotDir
	case errors.Is(err, errNotFile):
		return CodeNotFile
	case errors.Is(err, errExists):
		return CodeExists
	case errors.Is(err, errProtected):
		return CodeProtected
	}
	if c := platformCode(err); c != "" {
		return c
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, fs.ErrPermission):
		return CodeDenied
	case errors.Is(err, fs.ErrExist):
		return CodeExists
	}
	return CodeIO
}
