package winsvc

import (
	"strings"

	"github.com/ruolez/remotectl/proto"
)

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

// regKey is written by the MSI (EnrollToken, ServerURL, InstallSource).
const regKey = `SOFTWARE\RemoteAgent`

type enrollSource int

const (
	fromNone enrollSource = iota
	fromFlag
	fromRegistry
	fromInstallLink
)

// enrollCode picks what the service enrolls with: the -token flag, the MSI's
// ENROLL_TOKEN, else the install link code in the name of the MSI it was
// installed from (InstallSource = [OriginalDatabase]).
func enrollCode(flag, regToken, installSource string) (string, enrollSource) {
	switch {
	case flag != "":
		return flag, fromFlag
	case regToken != "":
		return regToken, fromRegistry
	}
	if code, ok := proto.InstallCodeFromPath(installSource); ok {
		return code, fromInstallLink
	}
	return "", fromNone
}
