// Package platform is the boundary between the portable agent core and the
// OS-specific native layer. internal/native implements Backend over the C ABI
// in native/include/rc_native.h; tests use fakes.
package platform

import "errors"

var (
	ErrPermission  = errors.New("permission not granted")
	ErrUnsupported = errors.New("unsupported on this platform")
	ErrBusy        = errors.New("capture already running")
)

type Monitor struct {
	ID      uint32
	Name    string
	X, Y    int32
	Width   uint32
	Height  uint32
	Scale   float32
	Primary bool
}

type Permissions struct {
	Screen bool
	Input  bool
}

type CaptureConfig struct {
	MonitorID          uint32
	FPS                int
	BitrateKbps        int
	KeyframeIntervalMS int
	MaxWidth           int
	MaxHeight          int
}

// CaptureSink receives native callbacks. Implementations must be fast and
// must not block (they run on native threads).
type CaptureSink interface {
	// Frame is one H.264 access unit, Annex-B. data is only valid during the call.
	Frame(data []byte, ptsMicros int64, keyframe bool)
	// CursorShape: RGBA8, w*h*4 bytes. data is only valid during the call.
	CursorShape(id uint32, w, h, hotX, hotY uint16, rgba []byte)
	// CursorPos: normalized 0..65535 within the captured monitor.
	CursorPos(x, y uint16, visible bool)
}

const (
	EvMouseMove    = 1
	EvMouseButton  = 2
	EvMouseWheel   = 3
	EvKey          = 4
	EvMouseMoveRel = 6
)

const (
	BtnLeft    = 0
	BtnRight   = 1
	BtnMiddle  = 2
	BtnBack    = 3
	BtnForward = 4
)

const KeyFlagExtended = 0x1

// InputEvent mirrors rc_input_event. Keycode is a platform key code produced
// by internal/keymap from a USB HID usage.
type InputEvent struct {
	Type      uint8
	Button    uint8
	Down      bool
	Flags     uint8
	MonitorID uint32
	Keycode   uint32
	X, Y      uint16
	DX, DY    int16
}

// Consent results from a prompt on the device's own screen.
type Consent int

const (
	ConsentDeny     Consent = 0
	ConsentOnce     Consent = 1
	ConsentRemember Consent = 2
)

type Backend interface {
	Name() string // "macos" | "windows" | "linux-x11"
	Init() error
	Shutdown()
	Monitors() ([]Monitor, error)
	Permissions() Permissions
	RequestPermission(screen bool)

	StartCapture(cfg CaptureConfig, sink CaptureSink) error
	StopCapture()
	ConfigureEncoder(bitrateKbps, fps int)
	RequestKeyframe()
	EncoderName() string

	Inject(ev InputEvent) error
	InjectText(s string) error

	ClipboardText() (string, error)
	SetClipboardText(s string) error

	// HideWallpaper hides or shows the desktop wallpaper in what the
	// controller sees; the state outlives capture restarts. ErrUnsupported
	// when the platform cannot hide it.
	HideWallpaper(hide bool) error

	// Indicator shows/hides the native always-on-top session border.
	Indicator(on bool)
	// ConsentPrompt blocks until the owner answers on the device screen or the
	// timeout passes. Returns ErrUnsupported when there is no native UI.
	ConsentPrompt(title, message string, timeoutSec int) (Consent, error)
}
