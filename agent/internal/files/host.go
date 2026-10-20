package files

import (
	"os"
	"path/filepath"
)

// Host runs file-system work as the device's signed-in user. On macOS and
// Linux the agent already is that user (LocalHost); the Windows service
// runs as SYSTEM and impersonates the console user (winsvc.UserFS).
type Host interface {
	// Do runs fn with the user's identity. Paths are opened inside fn;
	// reads and writes on handles opened there may happen outside it.
	Do(fn func() error) error
	// Roots lists the places the browser may start from and the default
	// upload directory. ErrNoUser when nobody is signed in.
	Roots() (roots []Root, defaultDir string, err error)
}

// LocalHost is the Host for agents running as the user (macOS, Linux).
type LocalHost struct{}

func (LocalHost) Do(fn func() error) error { return fn() }

func (LocalHost) Roots() ([]Root, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	roots, def := userRoots(home, isDir)
	return append(roots, volumeRoots()...), def, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// userRoots lists home and its standard folders that exist; the default
// upload directory is Downloads, else home.
func userRoots(home string, exists func(string) bool) ([]Root, string) {
	roots := []Root{{Name: "Home", Path: home, Kind: "home"}}
	def := home
	for _, f := range []struct{ name, kind string }{{"Desktop", "desktop"}, {"Documents", "documents"}, {"Downloads", "downloads"}} {
		p := filepath.Join(home, f.name)
		if !exists(p) {
			continue
		}
		roots = append(roots, Root{Name: f.name, Path: p, Kind: f.kind})
		if f.kind == "downloads" {
			def = p
		}
	}
	return roots, def
}
