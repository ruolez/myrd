// Package platform is the boundary between the portable agent core and the
// OS-specific native layer. internal/native implements Backend over the C ABI
// in native/include/rc_native.h; tests use fakes.
package platform

import "errors"

var (
	ErrPermission  = errors.New("permission not granted")
	ErrUnsupported = errors.New("unsupported on this platform")
	ErrBusy        = errors.New("capture already running")
	ErrLocked      = errors.New("the screen is locked")
	ErrAsleep      = errors.New("the display is asleep")
)

// Image is a BGRA picture, top-down (Snapshot).
type Image struct {
	Pix           []byte
	Width, Height int
	Stride        int
}

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
	// ProfileIDC is the negotiated H.264 profile (66 constrained baseline,
	// 77 main, 100 constrained high); the encoder uses at most that.
	ProfileIDC int
	Codec      int // CodecH264 or CodecVP9
}

// RefineLossless is RefineEncoder's maxQP for a lossless pass
// (rc_native.h RC_REFINE_LOSSLESS).
const RefineLossless = -2

// Codecs a capture can encode (rc_native.h RC_CODEC_*).
const (
	CodecH264 = 0
	CodecVP9  = 1 // VP9 profile 1, 4:4:4: full color
)

// FrameFlags describe an encoded frame (rc_native.h RC_FRAME_*).
type FrameFlags uint8

const (
	FrameKey    FrameFlags = 0x1 // IDR / VP9 keyframe
	FrameStatic FrameFlags = 0x2 // no new screen content: a refinement pass, an idle repeat
	FrameMinor  FrameFlags = 0x4 // new content in a small area only (a caret, a clock)
)

// CaptureSink receives native callbacks. Implementations must be fast and
// must not block (they run on native threads).
type CaptureSink interface {
	// Frame is one H.264 access unit, Annex-B (one VP9 frame with CodecVP9).
	// data is only valid during the call.
	Frame(data []byte, ptsMicros int64, flags FrameFlags)
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

// UnlockPrompt is how ready the unlock screen is for a password.
type UnlockPrompt int

const (
	// PromptUnknown: the backend cannot tell.
	PromptUnknown UnlockPrompt = iota
	// PromptHidden: no password field yet (the Windows lock curtain, a
	// sleeping macOS display).
	PromptHidden
	// PromptReady: Windows: a password field has the keyboard focus. macOS:
	// the display is awake.
	PromptReady
)

func (p UnlockPrompt) String() string {
	switch p {
	case PromptHidden:
		return "hidden"
	case PromptReady:
		return "ready"
	}
	return "unknown"
}

// Consent results from a prompt on the device's own screen.
type Consent int

const (
	ConsentDeny     Consent = 0
	ConsentOnce     Consent = 1
	ConsentRemember Consent = 2
)

// DisplayMode is a monitor mode as the OS's display settings name it: pixels,
// points ("looks like") on macOS, where Scale is the pixels per point.
type DisplayMode struct {
	Width, Height uint32
	Scale         float32
}

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
	// RefineEncoder re-encodes the last frame with its quantizer at most
	// maxQP (static-screen refinement); maxQP 0 is a repeat pass under the
	// normal rate control, RefineLossless a lossless one. ErrUnsupported when
	// maxQP > 0 and the encoder cannot bound its quantizer, or for a lossless
	// pass outside VP9.
	RefineEncoder(maxQP int) error
	EncoderName() string
	// SupportsCodec reports whether captures can use codec (Codec*).
	SupportsCodec(codec int) bool

	Inject(ev InputEvent) error
	InjectText(s string) error

	ClipboardText() (string, error)
	SetClipboardText(s string) error

	// HideWallpaper hides or shows the desktop wallpaper in what the
	// controller sees; the state outlives capture restarts. ErrUnsupported
	// when the platform cannot hide it.
	HideWallpaper(hide bool) error

	// DisplayModes lists the modes a monitor (0 = primary) can switch to,
	// largest first, one per size, and its current mode. ErrUnsupported where
	// modes cannot be changed (rc_native.h rc_display_modes).
	DisplayModes(monitorID uint32) ([]DisplayMode, DisplayMode, error)
	// SetDisplayMode switches a monitor to width×height for this login
	// session only; capture must be stopped and the monitors listed again.
	SetDisplayMode(monitorID uint32, width, height uint32) error
	// RestoreDisplays brings back the saved mode of every monitor.
	RestoreDisplays() error

	// Snapshot takes one picture of a monitor (0 = primary) scaled down to
	// fit maxW×maxH, without starting a capture, for a dashboard preview.
	// ErrBusy while a capture runs, ErrPermission, ErrLocked, ErrAsleep or
	// ErrUnsupported (rc_native.h rc_snapshot).
	Snapshot(monitorID uint32, maxW, maxH int) (Image, error)

	// ScreenLocked reports whether the device shows its unlock screen (a
	// logged-in session that is locked); false where the backend cannot tell.
	ScreenLocked() bool
	// UnlockPrompt reports whether the unlock screen takes a password now,
	// with what has the keyboard focus for the log (Windows).
	UnlockPrompt() (UnlockPrompt, string)

	// Indicator shows/hides the native always-on-top session border.
	Indicator(on bool)
	// ConsentPrompt blocks until the owner answers on the device screen or the
	// timeout passes. Returns ErrUnsupported when there is no native UI.
	ConsentPrompt(title, message string, timeoutSec int) (Consent, error)
}
