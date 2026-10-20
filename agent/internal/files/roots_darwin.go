package files

import (
	"os"
	"path/filepath"
)

// volumeRoots lists mounted volumes; the boot volume's /Volumes entry is a
// symlink to "/".
func volumeRoots() []Root {
	des, _ := os.ReadDir("/Volumes")
	var out []Root
	seen := map[string]bool{}
	for _, de := range des {
		if de.Name()[0] == '.' {
			continue
		}
		p := filepath.Join("/Volumes", de.Name())
		if r, err := filepath.EvalSymlinks(p); err == nil && r == "/" {
			p = "/"
		}
		if seen[p] || !isDir(p) {
			continue
		}
		seen[p] = true
		out = append(out, Root{Name: de.Name(), Path: p, Kind: "volume"})
	}
	if !seen["/"] {
		out = append([]Root{{Name: "Macintosh HD", Path: "/", Kind: "volume"}}, out...)
	}
	return out
}
