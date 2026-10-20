//go:build !darwin

package update

import (
	"context"
	"errors"
	"log/slog"
)

const SystemDaemonLog = ""

func RunSystemDaemon(context.Context, *slog.Logger) error {
	return errors.New("update-daemon is macOS only")
}
