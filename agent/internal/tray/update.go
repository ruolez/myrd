package tray

import "github.com/ruolez/remotectl/proto"

const maxUpdateError = 60

// UpdateLine is the tray's update line for an update.status ("" hides it).
func UpdateLine(st proto.UpdateStatus) string {
	target := "Update"
	if st.Version != "" {
		target = "Update to " + st.Version
	}
	switch st.State {
	case proto.UpdateChecking:
		return "Checking for updates…"
	case proto.UpdateDownloading:
		return "Downloading update " + st.Version + "…"
	case proto.UpdateWaitingIdle:
		return target + " waits for the session to end"
	case proto.UpdateInstalling:
		return "Updating to " + st.Version + "…"
	case proto.UpdateFailed:
		return target + " failed: " + clip(st.Error)
	case proto.UpdateRolledBack:
		return target + " rolled back: " + clip(st.Error)
	}
	return ""
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxUpdateError {
		return s
	}
	return string(r[:maxUpdateError-1]) + "…"
}
