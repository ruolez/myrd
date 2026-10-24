//go:build !windows || win7

package vdd

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ruolez/remotectl/proto"
)

var errUnavailable = errors.New("the virtual display needs 64-bit Windows 10 1803 or later")

// Available reports whether this system can run the driver: never here.
func Available() bool { return false }

// Info is nil: device.info carries no virtual_display.
func Info() *proto.VirtualDisplayInfo { return nil }

func Install(*slog.Logger) error         { return errUnavailable }
func Remove(*slog.Logger) error          { return errUnavailable }
func Keep(context.Context, *slog.Logger) {}
