package winsvc

import "github.com/ruolez/remotectl/agent/internal/files"

// Windows GetDriveType results that are offered as roots.
const (
	driveRemovable = 2
	driveFixed     = 3
	driveRemote    = 4 // a mapped network drive (a drive path, not UNC)
	driveCDROM     = 5
	driveRAMDisk   = 6
)

// driveRoots lists the drive letters in GetLogicalDrives' mask whose type
// is browsable; typeOf is GetDriveType for a root like `C:\`.
func driveRoots(mask uint32, typeOf func(root string) uint32) []files.Root {
	var out []files.Root
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		switch typeOf(root) {
		case driveRemovable, driveFixed, driveRemote, driveCDROM, driveRAMDisk:
			out = append(out, files.Root{Name: root[:2], Path: root, Kind: "drive"})
		}
	}
	return out
}
