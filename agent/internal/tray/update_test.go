package tray

import (
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func TestUpdateLine(t *testing.T) {
	long := strings.Repeat("x", 200)
	cases := []struct {
		st   proto.UpdateStatus
		want string
	}{
		{proto.UpdateStatus{}, ""},
		{proto.UpdateStatus{State: proto.UpdateChecking}, "Checking for updates…"},
		{proto.UpdateStatus{State: proto.UpdateDownloading, Version: "0.3.1"}, "Downloading update 0.3.1…"},
		{proto.UpdateStatus{State: proto.UpdateWaitingIdle, Version: "0.3.1"}, "Update to 0.3.1 waits for the session to end"},
		{proto.UpdateStatus{State: proto.UpdateInstalling, Version: "0.3.1"}, "Updating to 0.3.1…"},
		{proto.UpdateStatus{State: proto.UpdateUpToDate}, ""},
		{proto.UpdateStatus{State: proto.UpdateFailed, Version: "0.3.1", Error: "installer exited with 1603"},
			"Update to 0.3.1 failed: installer exited with 1603"},
		{proto.UpdateStatus{State: proto.UpdateFailed, Error: "not connected"}, "Update failed: not connected"},
		{proto.UpdateStatus{State: proto.UpdateRolledBack, Version: "0.3.1", Error: "no hello_ack"},
			"Update to 0.3.1 rolled back: no hello_ack"},
		{proto.UpdateStatus{State: proto.UpdateFailed, Version: "0.3.1", Error: long},
			"Update to 0.3.1 failed: " + strings.Repeat("x", 59) + "…"},
		{proto.UpdateStatus{State: "bogus"}, ""},
	}
	for _, c := range cases {
		if got := UpdateLine(c.st); got != c.want {
			t.Errorf("UpdateLine(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}
