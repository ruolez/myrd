package files

import (
	"os"
	"os/user"
	"path/filepath"
)

// volumeRoots lists the file-system root and removable media mounted under
// /media/<user> (udisks) or /run/media/<user>.
func volumeRoots() []Root {
	out := []Root{{Name: "Computer", Path: "/", Kind: "volume"}}
	u, err := user.Current()
	if err != nil {
		return out
	}
	for _, base := range []string{filepath.Join("/media", u.Username), filepath.Join("/run/media", u.Username)} {
		des, _ := os.ReadDir(base)
		for _, de := range des {
			if p := filepath.Join(base, de.Name()); isDir(p) {
				out = append(out, Root{Name: de.Name(), Path: p, Kind: "volume"})
			}
		}
	}
	return out
}
