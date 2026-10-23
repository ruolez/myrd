//go:build !windows || !cgo

package native

import "log/slog"

// SetLogger: only the Windows backend reports diagnostics this way.
func SetLogger(*slog.Logger) {}
