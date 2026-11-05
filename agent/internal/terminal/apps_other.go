//go:build !darwin && !windows

package terminal

// Terminal app tabs are mirrored on macOS only.
func appSources() []Source { return nil }

func (l *Local) attachApp(string, int, int) (Stream, Source, error) {
	return nil, Source{}, ErrNotFound
}
