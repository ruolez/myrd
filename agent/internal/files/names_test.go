package files

import (
	"strings"
	"testing"
	"testing/quick"
	"time"
	"unicode/utf8"
)

func TestSanitizeName(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct {
		in, want string
		bad      bool
	}{
		{in: "report.pdf", want: "report.pdf"},
		{in: "../../etc/passwd", want: "passwd"},
		{in: `C:\Users\bob\notes.txt`, want: "notes.txt"},
		{in: "a<b>c:d\"e|f?g*h.txt", want: "a_b_c_d_e_f_g_h.txt"},
		{in: "tab\there\x00.txt", want: "tabhere.txt"},
		{in: "bell\x7f\u0085.txt", want: "bell.txt"},
		{in: "bad\xffutf8.txt", want: "badutf8.txt"},
		{in: "trailing. . ", want: "trailing"},
		{in: "  spaced.txt  ", want: "spaced.txt"},
		{in: ".bashrc", want: ".bashrc"},
		{in: "CON", want: "_CON"},
		{in: "con.txt", want: "_con.txt"},
		{in: "Lpt9.tar.gz", want: "_Lpt9.tar.gz"},
		{in: "COM¹.log", want: "_COM¹.log"},
		{in: "CONSOLE.txt", want: "CONSOLE.txt"},
		{in: "über ✓.txt", want: "über ✓.txt"},
		{in: long + ".txt", want: strings.Repeat("a", 251) + ".txt"},
		{in: long + ".tar.gz", want: strings.Repeat("a", 248) + ".tar.gz"},
		{in: strings.Repeat("é", 200) + ".md", want: strings.Repeat("é", 126) + ".md"},
		{in: "", bad: true},
		{in: ".", bad: true},
		{in: "..", bad: true},
		{in: "dir/", bad: true},
		{in: " . ", bad: true},
		{in: "\x01\x02", bad: true},
		{in: ".a.txt.remotectl-7.part", bad: true},
	}
	for _, c := range cases {
		got, err := SanitizeName(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("SanitizeName(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("SanitizeName(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestSanitizeNameProperties(t *testing.T) {
	f := func(in string) bool {
		got, err := SanitizeName(in)
		if err != nil {
			return true
		}
		again, err2 := SanitizeName(got)
		return err2 == nil && again == got && len(got) <= maxNameBytes && utf8.ValidString(got) &&
			!strings.ContainsAny(got, "/\\<>:\"|?*\x00") && got != "." && got != ".."
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
}

func TestSplitExt(t *testing.T) {
	cases := []struct{ in, stem, ext string }{
		{"a.txt", "a", ".txt"},
		{"a.b.c", "a.b", ".c"},
		{"backup.tar.gz", "backup", ".tar.gz"},
		{"BACKUP.TAR.XZ", "BACKUP", ".TAR.XZ"},
		{".tar.gz", ".tar", ".gz"},
		{".bashrc", ".bashrc", ""},
		{"noext", "noext", ""},
		{"dot.", "dot.", ""},
	}
	for _, c := range cases {
		if s, e := splitExt(c.in); s != c.stem || e != c.ext {
			t.Errorf("splitExt(%q) = %q %q, want %q %q", c.in, s, e, c.stem, c.ext)
		}
	}
}

func TestUniqueName(t *testing.T) {
	set := func(names ...string) func(string) bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return func(s string) bool { return m[s] }
	}
	long := strings.Repeat("b", 251) + ".txt"
	cases := []struct {
		name  string
		taken []string
		want  string
	}{
		{"a.txt", nil, "a.txt"},
		{"a.txt", []string{"a.txt"}, "a (1).txt"},
		{"a.txt", []string{"a.txt", "a (1).txt", "a (2).txt"}, "a (3).txt"},
		{"x.tar.gz", []string{"x.tar.gz"}, "x (1).tar.gz"},
		{".env", []string{".env"}, ".env (1)"},
		{"README", []string{"README"}, "README (1)"},
		{long, []string{long}, strings.Repeat("b", 247) + " (1).txt"},
	}
	for _, c := range cases {
		got, err := UniqueName(c.name, set(c.taken...))
		if err != nil || got != c.want {
			t.Errorf("UniqueName(%q, %v) = %q, %v; want %q", c.name, c.taken, got, err, c.want)
		}
		if len(got) > maxNameBytes {
			t.Errorf("UniqueName(%q) is %d bytes", c.name, len(got))
		}
	}
	if _, err := UniqueName("a", func(string) bool { return true }); err == nil {
		t.Fatal("UniqueName with everything taken did not fail")
	}
}

func TestPartNames(t *testing.T) {
	cases := []struct {
		name string
		xfer uint32
		want string
	}{
		{"a.txt", 7, ".a.txt.remotectl-7.part"},
		{"a.txt", 4294967295, ".a.txt.remotectl-4294967295.part"},
	}
	for _, c := range cases {
		if got := PartName(c.name, c.xfer); got != c.want || !IsPartName(got) {
			t.Errorf("PartName(%q, %d) = %q (IsPartName %v), want %q", c.name, c.xfer, got, IsPartName(got), c.want)
		}
	}
	if p := PartName(strings.Repeat("z", 255), 1); len(p) > maxNameBytes || !IsPartName(p) {
		t.Errorf("long part name %d bytes, IsPartName %v", len(p), IsPartName(p))
	}
	for _, n := range []string{"a.part", ".a.part", ".remotectl-1.part", ".a.remotectl-x.part", ".a.remotectl-1.parts", "a.remotectl-1.part"} {
		if IsPartName(n) {
			t.Errorf("IsPartName(%q) = true", n)
		}
	}
}

func TestIsStalePart(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	part := PartName("a.txt", 3)
	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{part, StalePartAge + time.Second, true},
		{part, StalePartAge - time.Second, false},
		{"a.txt", 48 * time.Hour, false},
	}
	for _, c := range cases {
		if got := IsStalePart(c.name, now.Add(-c.age), now); got != c.want {
			t.Errorf("IsStalePart(%q, age %v) = %v", c.name, c.age, got)
		}
	}
}
