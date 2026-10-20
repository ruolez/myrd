package agent

import (
	"errors"
	"testing"
)

func TestEnrollPromptMessage(t *testing.T) {
	const ask = "Enter the enrollment token shown in the web app under Devices → Add device."
	cases := []struct {
		name    string
		server  string
		lastErr error
		want    string
	}{
		{"first prompt", "https://myrd.site", nil, ask + "\n\nServer: https://myrd.site"},
		{"retry shows the error", "https://myrd.site", errors.New("token expired"),
			ask + "\n\nServer: https://myrd.site\n\nEnrollment failed: token expired"},
		{"no server built in", "", nil,
			ask + "\n\nThis installer has no server address built in. Download it again from your server's Add device page."},
	}
	for _, c := range cases {
		if got := enrollPromptMessage(c.server, c.lastErr); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
