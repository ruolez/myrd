package files

import (
	"errors"
	"path"
	"strings"
)

var (
	ErrBadPath = errors.New("bad path")
	ErrDenied  = errors.New("path denied")
)

// CheckPath validates a browser-supplied absolute path for goos and returns
// it cleaned ("." and ".." resolved lexically, separators normalized).
// Windows paths must be drive paths ("C:\..."); UNC, device ("\\?\",
// "\\.\") and alternate-data-stream paths are rejected, as are components
// Windows would silently rewrite (trailing dots or spaces, device names).
func CheckPath(p, goos string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", ErrBadPath
	}
	if goos != "windows" {
		if !strings.HasPrefix(p, "/") {
			return "", ErrBadPath
		}
		return path.Clean(p), nil
	}
	p = strings.ReplaceAll(p, "/", `\`)
	if len(p) < 3 || !isDriveLetter(p[0]) || p[1] != ':' || p[2] != '\\' {
		return "", ErrBadPath
	}
	var parts []string
	for _, c := range strings.Split(p[3:], `\`) {
		switch c {
		case "", ".":
			continue
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
			continue
		}
		if strings.ContainsAny(c, `:<>"|?*`) || strings.TrimRight(c, ". ") != c || hasControl(c) {
			return "", ErrBadPath
		}
		stem := c
		if i := strings.IndexByte(stem, '.'); i >= 0 {
			stem = stem[:i]
		}
		if windowsReserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
			return "", ErrBadPath
		}
		parts = append(parts, c)
	}
	return strings.ToUpper(p[:1]) + `:\` + strings.Join(parts, `\`), nil
}

func isDriveLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}

// ParentDir returns the parent of a cleaned absolute path, or "" for a
// file-system root ("/", "C:\").
func ParentDir(p, goos string) string {
	if goos == "windows" {
		if len(p) <= 3 {
			return ""
		}
		i := strings.LastIndexByte(p, '\\')
		if i <= 2 {
			return p[:3]
		}
		return p[:i]
	}
	if p == "/" {
		return ""
	}
	return path.Dir(p)
}

// JoinPath appends a name to a cleaned absolute directory.
func JoinPath(dir, name, goos string) string {
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	if strings.HasSuffix(dir, sep) {
		return dir + name
	}
	return dir + sep + name
}

// BaseName returns the last component of a cleaned absolute path.
func BaseName(p, goos string) string {
	sep := byte('/')
	if goos == "windows" {
		sep = '\\'
	}
	return p[strings.LastIndexByte(p, sep)+1:]
}

// Policy holds the agent-side deny list: the agent's own state directory
// (device key, pins, config) is never listed into, read or written.
type Policy struct {
	Deny []string
}

// Denied reports whether p (cleaned, symlinks resolved) is a denied path or
// lies under one. Comparison ignores case except on Linux, matching the
// default file systems of macOS and Windows.
func (pl Policy) Denied(p, goos string) bool {
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	fold := func(s string) string {
		if goos == "linux" {
			return s
		}
		return strings.ToLower(s)
	}
	p = fold(p)
	for _, d := range pl.Deny {
		if d == "" {
			continue
		}
		d = strings.TrimSuffix(fold(d), sep)
		if d == "" || p == d || strings.HasPrefix(p, d+sep) {
			return true
		}
	}
	return false
}
