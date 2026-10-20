//go:build tools

// Package deps pins module dependencies used by platform-specific files that
// are not compiled on every OS, so `go mod tidy` on any host keeps them.
package deps

import (
	_ "fyne.io/systray"
	_ "github.com/coder/websocket"
	_ "github.com/pion/interceptor"
	_ "github.com/pion/webrtc/v4"
	_ "golang.org/x/sys/windows"
	_ "golang.org/x/sys/windows/svc"
)
