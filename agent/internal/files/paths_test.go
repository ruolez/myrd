package files

import "testing"

func TestCheckPath(t *testing.T) {
	cases := []struct {
		goos, in, want string
		bad            bool
	}{
		{goos: "linux", in: "/home/device", want: "/home/device"},
		{goos: "linux", in: "/", want: "/"},
		{goos: "darwin", in: "/Users/bob/../alice/./Desktop/", want: "/Users/alice/Desktop"},
		{goos: "linux", in: "/../../etc", want: "/etc"},
		{goos: "linux", in: "//double//slash", want: "/double/slash"},
		{goos: "linux", in: `/back\slash`, want: `/back\slash`},
		{goos: "linux", in: "", bad: true},
		{goos: "linux", in: "relative/path", bad: true},
		{goos: "darwin", in: "~/Desktop", bad: true},
		{goos: "linux", in: "/nul\x00byte", bad: true},
		{goos: "windows", in: `C:\Users\bob`, want: `C:\Users\bob`},
		{goos: "windows", in: `c:/Users/bob/`, want: `C:\Users\bob`},
		{goos: "windows", in: `D:\`, want: `D:\`},
		{goos: "windows", in: `C:\a\..\..\b`, want: `C:\b`},
		{goos: "windows", in: `C:\a\.\b\\c`, want: `C:\a\b\c`},
		{goos: "windows", in: `C:\~$report.docx`, want: `C:\~$report.docx`},
		{goos: "windows", in: `C:`, bad: true},
		{goos: "windows", in: `C:relative`, bad: true},
		{goos: "windows", in: `\Users\bob`, bad: true},
		{goos: "windows", in: `\\server\share\x`, bad: true},
		{goos: "windows", in: `//server/share/x`, bad: true},
		{goos: "windows", in: `\\?\C:\Windows`, bad: true},
		{goos: "windows", in: `\\.\PhysicalDrive0`, bad: true},
		{goos: "windows", in: `C:\file.txt:secret`, bad: true},
		{goos: "windows", in: `C:\file.txt::$DATA`, bad: true},
		{goos: "windows", in: `C:\dir.\x`, bad: true},
		{goos: "windows", in: `C:\dir \x`, bad: true},
		{goos: "windows", in: `C:\temp\CON`, bad: true},
		{goos: "windows", in: `C:\temp\nul.txt`, bad: true},
		{goos: "windows", in: `C:\a?b`, bad: true},
		{goos: "windows", in: "C:\\a\x01b", bad: true},
		{goos: "windows", in: `1:\x`, bad: true},
	}
	for _, c := range cases {
		got, err := CheckPath(c.in, c.goos)
		if c.bad {
			if err == nil {
				t.Errorf("CheckPath(%q, %s) = %q, want error", c.in, c.goos, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("CheckPath(%q, %s) = %q, %v; want %q", c.in, c.goos, got, err, c.want)
		}
	}
}

func TestParentJoinBase(t *testing.T) {
	cases := []struct{ goos, p, parent, base string }{
		{"linux", "/", "", ""},
		{"linux", "/home", "/", "home"},
		{"linux", "/home/device", "/home", "device"},
		{"windows", `C:\`, "", ""},
		{"windows", `C:\Users`, `C:\`, "Users"},
		{"windows", `C:\Users\bob`, `C:\Users`, "bob"},
	}
	for _, c := range cases {
		if got := ParentDir(c.p, c.goos); got != c.parent {
			t.Errorf("ParentDir(%q) = %q, want %q", c.p, got, c.parent)
		}
		if got := BaseName(c.p, c.goos); got != c.base {
			t.Errorf("BaseName(%q) = %q, want %q", c.p, got, c.base)
		}
	}
	joins := []struct{ goos, dir, name, want string }{
		{"linux", "/", "a", "/a"},
		{"linux", "/home", "a", "/home/a"},
		{"windows", `C:\`, "a", `C:\a`},
		{"windows", `C:\Users`, "a", `C:\Users\a`},
	}
	for _, c := range joins {
		if got := JoinPath(c.dir, c.name, c.goos); got != c.want {
			t.Errorf("JoinPath(%q, %q) = %q, want %q", c.dir, c.name, got, c.want)
		}
	}
}

func TestPolicyDenied(t *testing.T) {
	cases := []struct {
		goos string
		deny []string
		p    string
		want bool
	}{
		{"linux", []string{"/data"}, "/data", true},
		{"linux", []string{"/data/"}, "/data/pinned-clients.json", true},
		{"linux", []string{"/data"}, "/database", false},
		{"linux", []string{"/data"}, "/DATA", false},
		{"linux", []string{"/data"}, "/", false},
		{"linux", nil, "/data", false},
		{"linux", []string{""}, "/data", false},
		{"darwin", []string{"/Users/bob/Library/Application Support/RemoteAgent"}, "/users/BOB/library/application support/remoteagent/device.key", true},
		{"windows", []string{`C:\ProgramData\remotectl`}, `c:\programdata\REMOTECTL\agent.json`, true},
		{"windows", []string{`C:\ProgramData\remotectl`}, `C:\ProgramData\remotectl2`, false},
		{"windows", []string{`C:\ProgramData\remotectl`}, `C:\ProgramData`, false},
	}
	for _, c := range cases {
		if got := (Policy{Deny: c.deny}).Denied(c.p, c.goos); got != c.want {
			t.Errorf("Denied(%v, %q, %s) = %v, want %v", c.deny, c.p, c.goos, got, c.want)
		}
	}
}
