package files

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	pageEntries = 500
	pageBytes   = 48 << 10
	scanCap     = 20000
)

// Entry is one directory entry of a `listing`.
type Entry struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // dir | file | other
	Link   bool   `json:"link"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"` // unix ms
	Hidden bool   `json:"hidden"`
}

const (
	KindDir   = "dir"
	KindFile  = "file"
	KindOther = "other"
)

// readDir scans up to limit entries of dir. Symlinks are followed for the
// kind and size (a broken link is "other") and flagged with Link.
func readDir(dir string, limit int) (entries []Entry, truncated bool, err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	for len(entries) <= limit {
		batch, err := f.ReadDir(256)
		for _, de := range batch {
			if len(entries) == limit {
				return entries, true, nil
			}
			entries = append(entries, entryOf(dir, de))
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	return entries, false, nil
}

func entryOf(dir string, de fs.DirEntry) Entry {
	e := Entry{Name: de.Name(), Kind: KindOther, Hidden: strings.HasPrefix(de.Name(), ".")}
	info, err := de.Info()
	if err != nil {
		return e
	}
	if hiddenAttr(info) {
		e.Hidden = true
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		e.Link = true
		if t, err := os.Stat(filepath.Join(dir, de.Name())); err == nil {
			info = t
		} else {
			e.MTime = info.ModTime().UnixMilli()
			return e
		}
	}
	e.MTime = info.ModTime().UnixMilli()
	switch {
	case info.IsDir():
		e.Kind = KindDir
	case info.Mode().IsRegular():
		e.Kind = KindFile
		e.Size = info.Size()
	}
	return e
}

// sortEntries orders directories first, then by natural name order.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		di, dj := es[i].Kind == KindDir, es[j].Kind == KindDir
		if di != dj {
			return di
		}
		return naturalLess(es[i].Name, es[j].Name)
	})
}

// naturalLess compares case-insensitively with digit runs compared by
// value ("file2" < "file10"); exact ties fall back to byte order.
func naturalLess(a, b string) bool {
	if c := naturalCompare(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			na, ra := digitRun(a)
			nb, rb := digitRun(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(ta) != len(tb) {
				return cmp(len(ta), len(tb))
			}
			if ta != tb {
				return strings.Compare(ta, tb)
			}
			a, b = ra, rb
			continue
		}
		ca, sa := utf8.DecodeRuneInString(a)
		cb, sb := utf8.DecodeRuneInString(b)
		if la, lb := unicode.ToLower(ca), unicode.ToLower(cb); la != lb {
			return cmp(int(la), int(lb))
		}
		a, b = a[sa:], b[sb:]
	}
	return cmp(len(a), len(b))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string) (run, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

func cmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// visible drops hidden entries unless they were asked for.
func visible(es []Entry, hidden bool) []Entry {
	if hidden {
		return es
	}
	out := es[:0:0]
	for _, e := range es {
		if !e.Hidden {
			out = append(out, e)
		}
	}
	return out
}

// page cuts one listing page at offset: at most pageEntries entries and
// about pageBytes of JSON. next is nil on the last page.
func page(es []Entry, offset int) (out []Entry, next *int) {
	if offset >= len(es) {
		return []Entry{}, nil
	}
	size := 0
	end := offset
	for end < len(es) && end-offset < pageEntries {
		b, _ := json.Marshal(es[end])
		if end > offset && size+len(b)+1 > pageBytes {
			break
		}
		size += len(b) + 1
		end++
	}
	if end < len(es) {
		next = &end
	}
	return es[offset:end], next
}
