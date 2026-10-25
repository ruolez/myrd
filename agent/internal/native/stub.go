//go:build !cgo

// Package native implements platform.Backend over native/include/rc_native.h.
// This file is the CGO_ENABLED=0 build: every call reports ErrUnsupported so
// the rest of the agent compiles and tests everywhere.
package native

import "github.com/ruolez/remotectl/agent/internal/platform"

type Backend struct{}

func New() platform.Backend { return Backend{} }

func InitCaptureWorker(int) error { return platform.ErrUnsupported }
func ReleaseMemory()              {}

func (Backend) Name() string                          { return "none" }
func (Backend) Init() error                           { return platform.ErrUnsupported }
func (Backend) Shutdown()                             {}
func (Backend) Monitors() ([]platform.Monitor, error) { return nil, platform.ErrUnsupported }
func (Backend) Permissions() platform.Permissions     { return platform.Permissions{} }
func (Backend) RequestPermission(bool)                {}
func (Backend) StartCapture(platform.CaptureConfig, platform.CaptureSink) error {
	return platform.ErrUnsupported
}
func (Backend) StopCapture()                     {}
func (Backend) ConfigureEncoder(int, int)        {}
func (Backend) RequestKeyframe()                 {}
func (Backend) RefineEncoder(int) error          { return platform.ErrUnsupported }
func (Backend) EncoderName() string              { return "none" }
func (Backend) SupportsCodec(int) bool           { return false }
func (Backend) Inject(platform.InputEvent) error { return platform.ErrUnsupported }
func (Backend) InjectText(string) error          { return platform.ErrUnsupported }
func (Backend) ClipboardText() (string, error)   { return "", platform.ErrUnsupported }
func (Backend) SetClipboardText(string) error    { return platform.ErrUnsupported }
func (Backend) HideWallpaper(bool) error         { return platform.ErrUnsupported }
func (Backend) ScreenLocked() bool               { return false }
func (Backend) DisplayModes(uint32) ([]platform.DisplayMode, platform.DisplayMode, error) {
	return nil, platform.DisplayMode{}, platform.ErrUnsupported
}
func (Backend) SetDisplayMode(uint32, uint32, uint32) error { return platform.ErrUnsupported }
func (Backend) RestoreDisplays() error                      { return platform.ErrUnsupported }
func (Backend) Snapshot(uint32, int, int) (platform.Image, error) {
	return platform.Image{}, platform.ErrUnsupported
}
func (Backend) Indicator(bool) {}
func (Backend) ConsentPrompt(string, string, int) (platform.Consent, error) {
	return platform.ConsentDeny, platform.ErrUnsupported
}
