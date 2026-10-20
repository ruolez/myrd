package files

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxNameBytes = 255
	partInfix    = ".remotectl-"
	partSuffix   = ".part"
	// StalePartAge is how old an abandoned upload part must be to be swept.
	StalePartAge = 24 * time.Hour
)

var ErrBadName = errors.New("bad file name")

// multiExts are compound extensions kept whole by UniqueName and the length
// cap ("a (1).tar.gz", not "a.tar (1).gz").
var multiExts = []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst", ".tar.lz", ".tar.lzma", ".tar.br"}

var windowsReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}

func init() {
	for _, p := range []string{"COM", "LPT"} {
		for _, d := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
			windowsReserved[p+d] = true
		}
	}
}

// SanitizeName turns a browser-supplied file name into one that is safe on
// every OS the agent runs on: the basename only, no control or reserved
// characters, no trailing dots or spaces, Windows device names prefixed with
// "_", at most 255 bytes with the extension kept.
func SanitizeName(name string) (string, error) {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			// invalid UTF-8 and control characters are dropped
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = trimName(b.String())
	if name == "" || IsPartName(name) {
		return "", ErrBadName
	}
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if windowsReserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		name = "_" + name
	}
	if name = trimName(capName(name, maxNameBytes)); name == "" {
		return "", ErrBadName
	}
	return name, nil
}

// trimName drops leading spaces and trailing dots and spaces (Windows
// silently strips the latter, so "a." and "a" would be the same file).
func trimName(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	return strings.TrimRightFunc(s, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
}

// splitExt splits off the extension, treating compound archive extensions
// as one and a leading dot (".bashrc") as part of the stem.
func splitExt(name string) (stem, ext string) {
	lower := strings.ToLower(name)
	for _, e := range multiExts {
		if strings.HasSuffix(lower, e) && len(name) > len(e) {
			return name[:len(name)-len(e)], name[len(name)-len(e):]
		}
	}
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return name, ""
	}
	return name[:i], name[i:]
}

// capName shortens name to max bytes, cutting the stem at a rune boundary
// and keeping the extension when it fits.
func capName(name string, max int) string {
	if len(name) <= max {
		return name
	}
	stem, ext := splitExt(name)
	if len(ext) >= max/2 {
		stem, ext = name, ""
	}
	return truncUTF8(stem, max-len(ext)) + ext
}

func truncUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// UniqueName returns name, or "stem (N)ext" with the smallest N ≥ 1 for
// which taken reports false.
func UniqueName(name string, taken func(string) bool) (string, error) {
	if !taken(name) {
		return name, nil
	}
	stem, ext := splitExt(name)
	for i := 1; i <= 9999; i++ {
		suffix := " (" + strconv.Itoa(i) + ")"
		c := truncUTF8(stem, maxNameBytes-len(suffix)-len(ext)) + suffix + ext
		if !taken(c) {
			return c, nil
		}
	}
	return "", ErrBadName
}

// PartName is the hidden temporary name an upload is written under until
// its checksum is verified.
func PartName(name string, xfer uint32) string {
	tail := partInfix + strconv.FormatUint(uint64(xfer), 10) + partSuffix
	return "." + truncUTF8(name, maxNameBytes-1-len(tail)) + tail
}

// IsPartName reports whether name has the PartName shape.
func IsPartName(name string) bool {
	if !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, partSuffix) {
		return false
	}
	body := strings.TrimSuffix(name, partSuffix)
	i := strings.LastIndex(body, partInfix)
	if i < 1 {
		return false
	}
	_, err := strconv.ParseUint(body[i+len(partInfix):], 10, 32)
	return err == nil
}

// IsStalePart reports whether a directory entry is an abandoned upload part
// old enough to delete.
func IsStalePart(name string, mtime, now time.Time) bool {
	return IsPartName(name) && now.Sub(mtime) > StalePartAge
}
