package proto

import "testing"

const testInstallCode = "K7P2-9XQM-ABCD"

func TestInstallLinkFilename(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"RemoteAgent-0.6.4-x64.msi", "RemoteAgent-0.6.4-x64.link-K7P2-9XQM-ABCD.msi"},
		{"RemoteAgent-0.6.4.pkg", "RemoteAgent-0.6.4.link-K7P2-9XQM-ABCD.pkg"},
		{"RemoteAgent", "RemoteAgent.link-K7P2-9XQM-ABCD"},
	} {
		if got := InstallLinkFilename(c.in, testInstallCode); got != c.want {
			t.Errorf("InstallLinkFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInstallCodeFromPath(t *testing.T) {
	for _, c := range []struct {
		path string
		code string
		ok   bool
	}{
		{`C:\Users\ann\Downloads\RemoteAgent-0.6.4-x64.link-K7P2-9XQM-ABCD.msi`, testInstallCode, true},
		{"/Users/ann/Downloads/RemoteAgent-0.6.4.link-K7P2-9XQM-ABCD.pkg", testInstallCode, true},
		// Browsers number repeated downloads; Installer may name the component inside the product archive.
		{`C:\Users\ann\Downloads\RemoteAgent-0.6.4-x64.link-K7P2-9XQM-ABCD (1).msi`, testInstallCode, true},
		{"/Users/ann/Downloads/RemoteAgent-0.6.4.link-K7P2-9XQM-ABCD.pkg#component.pkg", testInstallCode, true},
		{"/tmp/RemoteAgent.LINK-k7p2-9xqm-abcd.pkg", testInstallCode, true},
		{`C:\Users\ann\Downloads\RemoteAgent-0.6.4-x64.msi`, "", false},
		{"", "", false},
		// I, O, 0 and 1 are not in the code alphabet; a fourth group is not a code.
		{"RemoteAgent.link-K7P2-9XQM-ABC0.msi", "", false},
		{"RemoteAgent.link-K7P2-9XQM-ABCDE.msi", "", false},
		{"RemoteAgent.link-K7P2-9XQM.msi", "", false},
		// Only the file name counts, not a folder that happens to look like one.
		{"/Users/ann/x.link-K7P2-9XQM-ABCD/RemoteAgent-0.6.4.pkg", "", false},
	} {
		code, ok := InstallCodeFromPath(c.path)
		if code != c.code || ok != c.ok {
			t.Errorf("InstallCodeFromPath(%q) = %q, %v; want %q, %v", c.path, code, ok, c.code, c.ok)
		}
	}
}

func TestInstallCodeRoundTrip(t *testing.T) {
	for _, name := range []string{"RemoteAgent-0.6.4-x64.msi", "RemoteAgent-0.6.4-x64-win7.msi", "RemoteAgent-0.6.4.pkg"} {
		code, ok := InstallCodeFromPath(`C:\Downloads\` + InstallLinkFilename(name, testInstallCode))
		if !ok || code != testInstallCode {
			t.Errorf("%s: got %q, %v", name, code, ok)
		}
	}
}
