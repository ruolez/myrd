package winsvc

import "testing"

func TestEnrollWaitReason(t *testing.T) {
	const hint = ` in HKLM\SOFTWARE\RemoteAgent; install with msiexec /i RemoteAgent.msi ENROLL_TOKEN=XXXX-XXXX SERVER_URL=https://your-server /qn`
	cases := []struct{ token, url, want string }{
		{"AB12-CD34", "https://myrd.site", ""},
		{"", "https://myrd.site", "not enrolled: missing EnrollToken" + hint},
		{"AB12-CD34", "", "not enrolled: missing ServerURL" + hint},
		{"", "", "not enrolled: missing EnrollToken and ServerURL" + hint},
	}
	for _, c := range cases {
		if got := enrollWaitReason(c.token, c.url); got != c.want {
			t.Errorf("enrollWaitReason(%q, %q)\n got %q\nwant %q", c.token, c.url, got, c.want)
		}
	}
}

func TestEnrollCode(t *testing.T) {
	const linkMSI = `C:\Users\ann\Downloads\RemoteAgent-0.6.4-x64.link-K7P2-9XQM-ABCD.msi`
	cases := []struct {
		flag, regToken, installSource string
		code                          string
		from                          enrollSource
	}{
		{"AB12-CD34", "EF56-GH78", linkMSI, "AB12-CD34", fromFlag},
		{"", "EF56-GH78", linkMSI, "EF56-GH78", fromRegistry},
		{"", "", linkMSI, "K7P2-9XQM-ABCD", fromInstallLink},
		{"", "", `C:\Users\ann\Downloads\RemoteAgent-0.6.4-x64.msi`, "", fromNone},
		{"", "", "", "", fromNone},
	}
	for _, c := range cases {
		code, from := enrollCode(c.flag, c.regToken, c.installSource)
		if code != c.code || from != c.from {
			t.Errorf("enrollCode(%q, %q, %q) = %q, %v; want %q, %v", c.flag, c.regToken, c.installSource, code, from, c.code, c.from)
		}
	}
}
