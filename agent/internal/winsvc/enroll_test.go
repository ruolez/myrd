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
