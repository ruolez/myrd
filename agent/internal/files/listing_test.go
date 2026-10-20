package files

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/quick"
)

func TestNaturalLess(t *testing.T) {
	ordered := []string{"a", "A1", "a2", "a02b", "a10", "a10a", "B", "b2", "file 9.txt", "file 10.txt", "z", "Ä", "日本"}
	for i := 0; i+1 < len(ordered); i++ {
		if !naturalLess(ordered[i], ordered[i+1]) || naturalLess(ordered[i+1], ordered[i]) {
			t.Errorf("want %q < %q", ordered[i], ordered[i+1])
		}
	}
	shuffled := append([]string(nil), ordered...)
	sort.Slice(shuffled, func(i, j int) bool { return shuffled[i] > shuffled[j] })
	sort.Slice(shuffled, func(i, j int) bool { return naturalLess(shuffled[i], shuffled[j]) })
	if strings.Join(shuffled, "|") != strings.Join(ordered, "|") {
		t.Fatalf("sorted %v", shuffled)
	}
}

func TestNaturalLessIsStrictOrder(t *testing.T) {
	irreflexive := func(a string) bool { return !naturalLess(a, a) }
	asymmetric := func(a, b string) bool { return !(naturalLess(a, b) && naturalLess(b, a)) }
	total := func(a, b string) bool { return a == b || naturalLess(a, b) || naturalLess(b, a) }
	transitive := func(a, b, c string) bool { return !(naturalLess(a, b) && naturalLess(b, c)) || naturalLess(a, c) }
	for name, f := range map[string]any{"irreflexive": irreflexive, "asymmetric": asymmetric, "total": total, "transitive": transitive} {
		if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSortEntriesDirsFirst(t *testing.T) {
	es := []Entry{{Name: "b.txt", Kind: KindFile}, {Name: "z", Kind: KindDir}, {Name: "a", Kind: KindOther}, {Name: "c", Kind: KindDir}}
	sortEntries(es)
	var got []string
	for _, e := range es {
		got = append(got, e.Name)
	}
	if strings.Join(got, ",") != "c,z,a,b.txt" {
		t.Fatalf("order %v", got)
	}
}

func TestPage(t *testing.T) {
	mk := func(n, nameLen int) []Entry {
		es := make([]Entry, n)
		for i := range es {
			es[i] = Entry{Name: fmt.Sprintf("%0*d", nameLen, i), Kind: KindFile}
		}
		return es
	}
	ptr := func(i int) *int { return &i }
	cases := []struct {
		name     string
		es       []Entry
		offset   int
		wantLen  int
		wantNext *int
	}{
		{"empty", nil, 0, 0, nil},
		{"one page", mk(10, 4), 0, 10, nil},
		{"entry cap", mk(1200, 4), 0, pageEntries, ptr(pageEntries)},
		{"middle page", mk(1200, 4), 500, 500, ptr(1000)},
		{"last page", mk(1200, 4), 1000, 200, nil},
		{"offset past end", mk(3, 4), 9, 0, nil},
	}
	for _, c := range cases {
		out, next := page(c.es, c.offset)
		if len(out) != c.wantLen || (next == nil) != (c.wantNext == nil) || (next != nil && *next != *c.wantNext) {
			t.Errorf("%s: %d entries, next %v; want %d, %v", c.name, len(out), next, c.wantLen, c.wantNext)
		}
	}
	// Long names cut the page by size, and every page still makes progress.
	es := mk(400, 250)
	for off := 0; ; {
		out, next := page(es, off)
		b, _ := json.Marshal(out)
		if len(b) > pageBytes+len(out) || len(out) == 0 {
			t.Fatalf("page at %d: %d entries, %d bytes", off, len(out), len(b))
		}
		if next == nil {
			break
		}
		off = *next
	}
}

func TestReadDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("12345"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".dot"), nil, 0o644)
	_ = os.Symlink("f.txt", filepath.Join(dir, "lf"))
	_ = os.Symlink("missing", filepath.Join(dir, "dangling"))
	es, truncated, err := readDir(dir, scanCap)
	if err != nil || truncated {
		t.Fatalf("readDir: %v %v", truncated, err)
	}
	sortEntries(es)
	var got []string
	for _, e := range es {
		got = append(got, fmt.Sprintf("%s/%s/%v/%d/%v", e.Name, e.Kind, e.Link, e.Size, e.Hidden))
		if e.MTime == 0 {
			t.Errorf("%s: no mtime", e.Name)
		}
	}
	want := "sub/dir/false/0/false .dot/file/false/0/true dangling/other/true/0/false f.txt/file/false/5/false lf/file/true/5/false"
	if strings.Join(got, " ") != want {
		t.Fatalf("entries\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("setup")
	}
	if _, _, err := readDir(filepath.Join(dir, "nope"), scanCap); errCode(err) != CodeNotFound {
		t.Fatalf("missing dir: %v (%s)", err, errCode(err))
	}
}

func TestReadDirScanCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 600; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0o644)
	}
	cases := []struct {
		limit     int
		n         int
		truncated bool
	}{{599, 599, true}, {600, 600, false}, {256, 256, true}, {1000, 600, false}}
	for _, c := range cases {
		es, truncated, err := readDir(dir, c.limit)
		if err != nil || len(es) != c.n || truncated != c.truncated {
			t.Errorf("limit %d: %d entries, truncated %v, %v; want %d %v", c.limit, len(es), truncated, err, c.n, c.truncated)
		}
	}
}

func TestVisible(t *testing.T) {
	es := []Entry{{Name: "a"}, {Name: ".b", Hidden: true}, {Name: "c"}}
	if got := visible(es, false); len(got) != 2 || got[1].Name != "c" {
		t.Fatalf("visible = %v", got)
	}
	if got := visible(es, true); len(got) != 3 {
		t.Fatalf("with hidden = %v", got)
	}
	if es[1].Name != ".b" {
		t.Fatal("visible modified its input")
	}
}

func TestUserRoots(t *testing.T) {
	has := func(names ...string) func(string) bool {
		return func(p string) bool {
			for _, n := range names {
				if filepath.Base(p) == n {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name   string
		exists func(string) bool
		kinds  string
		def    string
	}{
		{"all folders", has("Desktop", "Documents", "Downloads"), "home desktop documents downloads", "/h/Downloads"},
		{"no downloads", has("Desktop"), "home desktop", "/h"},
		{"none", has(), "home", "/h"},
	}
	for _, c := range cases {
		roots, def := userRoots("/h", c.exists)
		var kinds []string
		for _, r := range roots {
			kinds = append(kinds, r.Kind)
		}
		if strings.Join(kinds, " ") != c.kinds || def != filepath.FromSlash(c.def) {
			t.Errorf("%s: %v %q", c.name, kinds, def)
		}
	}
}
