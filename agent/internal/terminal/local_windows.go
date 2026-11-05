//go:build windows

package terminal

import "log/slog"

// Default is the terminal host of an agent running as the device's user.
// On Windows the session helper runs as SYSTEM and starts shells through
// the user's token instead (winsvc); nil here.
func Default(log *slog.Logger) (Host, func()) { return nil, func() {} }
