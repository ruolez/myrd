package app

import "testing"

func TestDownloadName(t *testing.T) {
	cases := []struct {
		os, raw, want string
		wantErr       bool
	}{
		{"macos", "RemoteAgent-1.2.0.pkg", "RemoteAgent-1.2.0.pkg", false},
		{"macos", "RemoteAgent.PKG", "RemoteAgent.PKG", false},
		{"windows", `C:\Users\me\Downloads\RemoteAgent.msi`, "RemoteAgent.msi", false},
		{"windows", "builds/remote-agent.exe", "remote-agent.exe", false},
		{"macos", "../../etc/agent.pkg", "agent.pkg", false},
		{"macos", "RemoteAgent.msi", "", true},
		{"windows", "RemoteAgent.pkg", "", true},
		{"linux", "agent.zip", "", true},
		{"macos", `evil".pkg`, "", true},
		{"macos", ".hidden.pkg", "", true},
		{"macos", "", "", true},
	}
	for _, c := range cases {
		got, err := downloadName(c.os, c.raw)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("downloadName(%q, %q) = %q, %v; want %q, err=%v", c.os, c.raw, got, err, c.want, c.wantErr)
		}
	}
}
