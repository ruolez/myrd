//go:build !windows

package files

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"syscall"
	"testing"
)

func TestErrCode(t *testing.T) {
	pathErr := func(e error) error { return &fs.PathError{Op: "open", Path: "/x", Err: e} }
	eperm := CodeDenied
	if runtime.GOOS == "darwin" {
		eperm = CodeOSDenied
	}
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{pathErr(syscall.ENOENT), CodeNotFound},
		{pathErr(syscall.ENOTDIR), CodeNotDir},
		{pathErr(syscall.EISDIR), CodeNotFile},
		{pathErr(syscall.ENOSPC), CodeNoSpace},
		{pathErr(syscall.EDQUOT), CodeNoSpace},
		{pathErr(syscall.EACCES), CodeDenied},
		{pathErr(syscall.EPERM), eperm},
		{pathErr(syscall.ENAMETOOLONG), CodeBadName},
		{fmt.Errorf("roots: %w", ErrNoUser), CodeNoUser},
		{ErrBadPath, CodeBadPath},
		{ErrBadName, CodeBadName},
		{ErrDenied, CodeDenied},
		{errNotDir, CodeNotDir},
		{errNotFile, CodeNotFile},
		{pathErr(syscall.EIO), CodeIO},
		{errors.New("boom"), CodeIO},
	}
	for _, c := range cases {
		if got := errCode(c.err); got != c.want {
			t.Errorf("errCode(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
