package proto

import (
	"strconv"
	"strings"
)

type version struct {
	core [3]uint64
	pre  []string
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !validIdents(s[i+1:], false) {
			return version{}, false
		}
		s = s[:i]
	}
	var v version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !validIdents(s[i+1:], true) {
			return version{}, false
		}
		v.pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	for i, p := range parts {
		if !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return version{}, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return version{}, false
		}
		v.core[i] = n
	}
	return v, true
}

// validIdents checks dot-separated SemVer identifiers; prerelease numeric
// identifiers must not have leading zeros.
func validIdents(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, r := range id {
			if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
		if pre && isNumeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidVersion reports whether s is a SemVer 2.0 version, optionally with a
// leading "v".
func ValidVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

// CompareVersions orders versions by SemVer 2.0 precedence: -1, 0 or +1.
// Build metadata is ignored and invalid versions sort before valid ones.
func CompareVersions(a, b string) int {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range va.core {
		if c := cmpUint(va.core[i], vb.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0
	case len(va.pre) == 0:
		return 1
	case len(vb.pre) == 0:
		return -1
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := cmpIdent(va.pre[i], vb.pre[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(va.pre)), uint64(len(vb.pre)))
}

func cmpIdent(a, b string) int {
	na, nb := isNumeric(a), isNumeric(b)
	switch {
	case na && nb:
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		return cmpUint(x, y)
	case na:
		return -1
	case nb:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
