//go:build !darwin && !windows

package terminal

import "log/slog"

// Terminal app tabs are mirrored on macOS only.
func appSources(*slog.Logger) ([]Source, []Blocked) { return nil, nil }

func askAppsAgain(*slog.Logger) {}

func (l *Local) attachApp(string, int, int) (Stream, Source, error) {
	return nil, Source{}, ErrNotFound
}
