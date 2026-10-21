//go:build cgo

// Package native implements platform.Backend over the C ABI in
// native/include/rc_native.h. Each OS links its own static library
// (flags_<os>.go).
package native

/*
#cgo CFLAGS: -I${SRCDIR}/../../../native/include
#include <stdlib.h>
#include <stdint.h>
#include "rc_native.h"

extern void rcGoFrame(void *user, uint8_t *data, int32_t len, int64_t pts_us, int32_t keyframe);
extern void rcGoCursorShape(void *user, uint32_t id, uint16_t w, uint16_t h, uint16_t hx, uint16_t hy, uint8_t *rgba, int32_t len);
extern void rcGoCursorPos(void *user, uint16_t x, uint16_t y, uint8_t visible);

static int rc_start_with_handle(const rc_capture_config *cfg, uintptr_t handle) {
	return rc_capture_start(cfg, (rc_frame_fn)rcGoFrame, (rc_cursor_shape_fn)rcGoCursorShape,
	                        (rc_cursor_pos_fn)rcGoCursorPos, (void *)handle);
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"sync"
	"unsafe"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Backend is the cgo-backed platform.Backend.
type Backend struct {
	mu     sync.Mutex
	handle cgo.Handle
	active bool
}

// New returns the native backend for this OS.
func New() platform.Backend { return &Backend{} }

func rcErr(rc C.int) error {
	switch rc {
	case C.RC_OK:
		return nil
	case C.RC_ERR_PERMISSION:
		return platform.ErrPermission
	case C.RC_ERR_UNSUPPORTED:
		return platform.ErrUnsupported
	case C.RC_ERR_BUSY:
		return platform.ErrBusy
	}
	if rc >= 0 {
		return nil
	}
	return fmt.Errorf("native error %d", int(rc))
}

func (b *Backend) Name() string { return C.GoString(C.rc_backend_name()) }
func (b *Backend) Init() error  { return rcErr(C.rc_init()) }
func (b *Backend) Shutdown() {
	b.StopCapture()
	C.rc_shutdown()
}

func (b *Backend) Monitors() ([]platform.Monitor, error) {
	var arr [16]C.rc_monitor
	n := C.rc_list_monitors(&arr[0], C.int(len(arr)))
	if n < 0 {
		return nil, rcErr(n)
	}
	out := make([]platform.Monitor, 0, int(n))
	for i := 0; i < int(n); i++ {
		m := arr[i]
		out = append(out, platform.Monitor{ID: uint32(m.id), Name: C.GoString(&m.name[0]), X: int32(m.x), Y: int32(m.y),
			Width: uint32(m.width), Height: uint32(m.height), Scale: float32(m.scale), Primary: m.primary != 0})
	}
	return out, nil
}

func (b *Backend) Permissions() platform.Permissions {
	var s, i C.int
	if C.rc_check_permissions(&s, &i) != C.RC_OK {
		return platform.Permissions{}
	}
	return platform.Permissions{Screen: s != 0, Input: i != 0}
}

func (b *Backend) RequestPermission(screen bool) {
	which := C.int(2)
	if screen {
		which = 1
	}
	C.rc_request_permission(which)
}

func (b *Backend) StartCapture(cfg platform.CaptureConfig, sink platform.CaptureSink) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active {
		return platform.ErrBusy
	}
	c := C.rc_capture_config{
		monitor_id: C.uint32_t(cfg.MonitorID), fps: C.int32_t(cfg.FPS), bitrate_kbps: C.int32_t(cfg.BitrateKbps),
		keyframe_interval_ms: C.int32_t(cfg.KeyframeIntervalMS), max_width: C.int32_t(cfg.MaxWidth), max_height: C.int32_t(cfg.MaxHeight),
		profile_idc: C.int32_t(cfg.ProfileIDC),
	}
	h := cgo.NewHandle(sink)
	if err := rcErr(C.rc_start_with_handle(&c, C.uintptr_t(h))); err != nil {
		h.Delete()
		return err
	}
	b.handle, b.active = h, true
	return nil
}

func (b *Backend) StopCapture() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}
	C.rc_capture_stop() // no callbacks after this returns
	b.handle.Delete()
	b.active = false
}

func (b *Backend) ConfigureEncoder(kbps, fps int) {
	C.rc_encode_configure(C.int32_t(kbps), C.int32_t(fps))
}
func (b *Backend) RequestKeyframe()    { C.rc_request_keyframe() }
func (b *Backend) EncoderName() string { return C.GoString(C.rc_encoder_name()) }

func (b *Backend) Inject(ev platform.InputEvent) error {
	var down C.uint8_t
	if ev.Down {
		down = 1
	}
	c := C.rc_input_event{_type: C.uint8_t(ev.Type), button: C.uint8_t(ev.Button), down: down, flags: C.uint8_t(ev.Flags),
		monitor_id: C.uint32_t(ev.MonitorID), keycode: C.uint32_t(ev.Keycode), x: C.uint16_t(ev.X), y: C.uint16_t(ev.Y),
		dx: C.int16_t(ev.DX), dy: C.int16_t(ev.DY)}
	return rcErr(C.rc_input_inject(&c))
}

func (b *Backend) InjectText(s string) error {
	if s == "" {
		return nil
	}
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return rcErr(C.rc_input_text(cs, C.int32_t(len(s))))
}

func (b *Backend) ClipboardText() (string, error) {
	var out *C.char
	var n C.int32_t
	if err := rcErr(C.rc_clipboard_get_text(&out, &n)); err != nil {
		return "", err
	}
	if out == nil {
		return "", nil
	}
	defer C.rc_free(unsafe.Pointer(out))
	return C.GoStringN(out, C.int(n)), nil
}

func (b *Backend) SetClipboardText(s string) error {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return rcErr(C.rc_clipboard_set_text(cs, C.int32_t(len(s))))
}

func (b *Backend) HideWallpaper(hide bool) error {
	v := C.int(0)
	if hide {
		v = 1
	}
	return rcErr(C.rc_wallpaper_hide(v))
}

func (b *Backend) Indicator(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.rc_indicator_show(v)
}

func (b *Backend) ConsentPrompt(title, message string, timeoutSec int) (platform.Consent, error) {
	ct, cm := C.CString(title), C.CString(message)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cm))
	rc := C.rc_consent_prompt(ct, cm, C.int32_t(timeoutSec))
	if rc < 0 {
		return platform.ConsentDeny, rcErr(rc)
	}
	switch rc {
	case 1:
		return platform.ConsentOnce, nil
	case 2:
		return platform.ConsentRemember, nil
	}
	return platform.ConsentDeny, nil
}

var _ platform.Backend = (*Backend)(nil)
