package proto

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Install links (PROTOCOL §2 "Install links"): the server names the installer
// it serves for a link "<name>.link-XXXX-XXXX-XXXX.<ext>", and the installer
// enrolls with the code it finds in its own file name. The code alphabet is
// the enrollment token one (no I, O, 0, 1).
const installLinkMarker = ".link-"

var installCodeInName = regexp.MustCompile(`(?i)\.link-([A-HJ-NP-Z2-9]{4})-([A-HJ-NP-Z2-9]{4})-([A-HJ-NP-Z2-9]{4})(?:[^A-Za-z0-9]|$)`)

// InstallLinkFilename puts an install link code into an installer's file name,
// before its extension: "RemoteAgent-0.6.4-x64.msi" → "RemoteAgent-0.6.4-x64.link-<code>.msi".
func InstallLinkFilename(filename, code string) string {
	ext := filepath.Ext(filename)
	return strings.TrimSuffix(filename, ext) + installLinkMarker + code + ext
}

// InstallCodeFromPath returns the code InstallLinkFilename put into the file
// name at the end of path (either separator), uppercased with dashes.
func InstallCodeFromPath(path string) (string, bool) {
	name := path[strings.LastIndexAny(path, `/\`)+1:]
	m := installCodeInName.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return strings.ToUpper(m[1] + "-" + m[2] + "-" + m[3]), true
}
