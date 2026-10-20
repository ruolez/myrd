package winsvc

import "strings"

// enrollWaitReason says why the service is not enrolling yet; "" when it has
// both a token and a server URL. Logged whenever it changes, so service.log
// explains a silent, unenrolled install.
func enrollWaitReason(token, serverURL string) string {
	var missing []string
	if token == "" {
		missing = append(missing, "EnrollToken")
	}
	if serverURL == "" {
		missing = append(missing, "ServerURL")
	}
	if len(missing) == 0 {
		return ""
	}
	return "not enrolled: missing " + strings.Join(missing, " and ") + ` in HKLM\` + regKey +
		"; install with msiexec /i RemoteAgent.msi ENROLL_TOKEN=XXXX-XXXX SERVER_URL=https://your-server /qn"
}

// regKey is written by the MSI (EnrollToken, ServerURL).
const regKey = `SOFTWARE\RemoteAgent`
