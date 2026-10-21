/*
 * rc_native.h — the one C ABI every OS backend implements.
 *
 *   native/macos   : Objective-C  (ScreenCaptureKit → VideoToolbox, CGEvent)
 *   native/windows : C++          (WGC / DXGI dup → Media Foundation, SendInput)
 *   native/linux   : C            (X11 XShm → x264, XTest)  — Docker demo device
 *
 * Each builds a static library named librcnative.a (rcnative.lib for MSVC)
 * that agent/internal/native links through cgo.
 *
 * Rules:
 *  - Go never touches raw pixels. Capture + encode happen here; the frame
 *    callback delivers one complete H.264 access unit in Annex-B form
 *    (start codes 00 00 00 01), or with RC_CODEC_VP9 one VP9 frame.
 *    Keyframes MUST carry SPS+PPS in-band.
 *  - H.264 at most rc_capture_config.profile_idc (constrained baseline,
 *    main or constrained high), no B-frames, no frame reordering,
 *    low-latency/real-time mode. Keyframes come from rc_request_keyframe;
 *    keyframe_interval_ms only bounds a lost request.
 *  - RC_CODEC_VP9 is full color: VP9 profile 1 (4:4:4) in software through
 *    native/common/rc_vpx.h, the same on every backend.
 *  - A frame without new screen content (a refinement pass, an idle repeat,
 *    a capture the OS reported changed whose pixels are not) carries
 *    RC_FRAME_STATIC; one that changed only a small area may carry
 *    RC_FRAME_MINOR. The agent restarts refinement on any other.
 *  - Callbacks may fire on any native thread; they must not be re-entered
 *    concurrently for the same kind (serialize frame callbacks).
 *  - After rc_capture_stop returns, no further callbacks may fire.
 *  - Coordinates crossing this ABI are normalized 0..65535 of the monitor.
 *  - Key codes crossing this ABI are already platform codes (the HID→platform
 *    mapping lives in Go: agent/internal/keymap). See rc_input_event.keycode.
 */
#ifndef RC_NATIVE_H
#define RC_NATIVE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define RC_OK               0
#define RC_ERR             -1
#define RC_ERR_PERMISSION  -2  /* screen or input permission not granted */
#define RC_ERR_UNSUPPORTED -3
#define RC_ERR_BUSY        -4  /* capture already running */

typedef struct {
    uint32_t id;        /* stable for the life of the process */
    int32_t  x, y;      /* origin in the virtual desktop, points/pixels as OS reports */
    uint32_t width;     /* capture size in physical pixels */
    uint32_t height;
    float    scale;     /* backing scale factor (2.0 on Retina), 1.0 elsewhere */
    uint8_t  primary;
    char     name[64];  /* UTF-8, NUL-terminated */
} rc_monitor;

/* ---- lifecycle ---- */
int  rc_init(void);
void rc_shutdown(void);
const char *rc_backend_name(void);   /* "macos" | "windows" | "linux-x11" */

/* Writes up to max monitors; returns the count (>=0) or an RC_ERR_*. */
int rc_list_monitors(rc_monitor *out, int max);

/* Sets *screen / *input to 1 if granted, 0 otherwise. */
int  rc_check_permissions(int *screen, int *input);
/* Triggers the OS prompt or opens the exact System Settings pane. */
void rc_request_permission(int which); /* 1 = screen, 2 = input */

/* ---- capture + encode ---- */
typedef struct {
    uint32_t monitor_id;
    int32_t  fps;                  /* target, <= 60 */
    int32_t  bitrate_kbps;         /* initial target */
    int32_t  keyframe_interval_ms; /* longest GOP; the agent sends 10 min */
    int32_t  max_width;            /* 0 = native; else downscale keeping aspect */
    int32_t  max_height;
    int32_t  profile_idc;          /* negotiated H.264 profile, the most the encoder may use:
                                      66 constrained baseline (also 0), 77 main, 100 constrained high */
    int32_t  codec;                /* RC_CODEC_* */
} rc_capture_config;

#define RC_CODEC_H264 0
#define RC_CODEC_VP9  1 /* VP9 profile 1, 4:4:4 */

/* Bitmask of (1 << RC_CODEC_*) this build can encode. */
int  rc_codecs(void);

#define RC_FRAME_KEY    0x1 /* IDR with SPS+PPS */
#define RC_FRAME_STATIC 0x2 /* same screen content as the frame before */
#define RC_FRAME_MINOR  0x4 /* new content in a small area only (about 2% of the
                               frame or less: a caret, a clock); optional */

/* One H.264 access unit, Annex-B. pts_us is a monotonic capture timestamp;
 * flags are RC_FRAME_*. */
typedef void (*rc_frame_fn)(void *user, const uint8_t *data, int32_t len,
                            int64_t pts_us, int32_t flags);
/* Cursor image changed. RGBA8 premultiplied=0, row stride = w*4. id is a
 * stable shape id so the browser can cache shapes. */
typedef void (*rc_cursor_shape_fn)(void *user, uint32_t id, uint16_t w, uint16_t h,
                                   uint16_t hot_x, uint16_t hot_y,
                                   const uint8_t *rgba, int32_t len);
/* Cursor moved, normalized 0..65535 within the captured monitor. */
typedef void (*rc_cursor_pos_fn)(void *user, uint16_t x, uint16_t y, uint8_t visible);

/* Starts capture of cfg->monitor_id. Returns RC_OK or RC_ERR_*.
 * The cursor MUST NOT be composited into the video. */
int  rc_capture_start(const rc_capture_config *cfg, rc_frame_fn on_frame,
                      rc_cursor_shape_fn on_shape, rc_cursor_pos_fn on_pos,
                      void *user);
void rc_capture_stop(void);
/* Live reconfigure; safe to call from any thread while capturing. */
void rc_encode_configure(int32_t bitrate_kbps, int32_t fps);
/* Next emitted frame must be an IDR keyframe (PLI). */
void rc_request_keyframe(void);
/* Static-screen refinement: re-encodes the last captured frame as a P-frame
 * with no macroblock above quantizer max_qp (1..51, H.264 scale; VP9 maps it
 * onto its own), flagged RC_FRAME_STATIC.
 * The clamp ends with that frame. Dropped silently when new content arrived
 * in the last 100 ms. Returns RC_OK, RC_ERR_UNSUPPORTED when the encoder
 * cannot bound its quantizer, or RC_ERR when not capturing. */
int  rc_encode_refine(int32_t max_qp);
/* "videotoolbox" | "mf-hw" | "mf-sw" | "x264" | "openh264" ...; "libvpx-vp9"
 * while an RC_CODEC_VP9 capture runs. */
const char *rc_encoder_name(void);

/* ---- input ---- */
#define RC_EV_MOUSE_MOVE      1  /* x,y normalized absolute on monitor_id */
#define RC_EV_MOUSE_BUTTON    2  /* button, down */
#define RC_EV_MOUSE_WHEEL     3  /* dx,dy in pixels; dy>0 scrolls content down (DOM deltaY) */
#define RC_EV_KEY             4  /* keycode (platform), down */
#define RC_EV_MOUSE_MOVE_REL  6  /* dx,dy relative pixels (Pointer Lock) */

#define RC_BTN_LEFT    0
#define RC_BTN_RIGHT   1
#define RC_BTN_MIDDLE  2
#define RC_BTN_BACK    3
#define RC_BTN_FORWARD 4

#define RC_KEYFLAG_EXTENDED 0x1  /* Windows: KEYEVENTF_EXTENDEDKEY scancode */

typedef struct {
    uint8_t  type;       /* RC_EV_* */
    uint8_t  button;     /* RC_BTN_* */
    uint8_t  down;       /* 1 = press, 0 = release */
    uint8_t  flags;      /* RC_KEYFLAG_* */
    uint32_t monitor_id;
    uint32_t keycode;    /* macOS: CGKeyCode (kVK_*); Windows: set-1 scancode;
                            Linux: X11 keycode (evdev + 8) */
    uint16_t x, y;       /* normalized 0..65535 */
    int16_t  dx, dy;
} rc_input_event;

int  rc_input_inject(const rc_input_event *ev);
/* Types a UTF-8 string with no key mapping (IME, emoji). */
int  rc_input_text(const char *utf8, int32_t len);

/* ---- clipboard (text in v1; png optional) ---- */
/* *out is allocated by the backend; free with rc_free. Returns RC_OK or RC_ERR. */
int  rc_clipboard_get_text(char **out, int32_t *len);
int  rc_clipboard_set_text(const char *utf8, int32_t len);
void rc_free(void *p);

/* ---- wallpaper ---- */
/* 1 hides the desktop wallpaper from the controller (solid background, far
 * cheaper to encode), 0 shows it again. Independent of rc_capture_start/stop:
 * the state holds until changed, and rc_shutdown shows it. 0 while it is not
 * hidden is a no-op, so the agent probes support with it.
 *   macos  : drops the wallpaper windows from the capture only; the device's
 *            own screen is untouched.
 *   windows: turns the wallpaper off for the console user's logon session
 *            (never written to the registry, so logoff restores it); 0 reloads
 *            it from the registry; rc_init reloads one a crashed agent left off.
 *   linux  : RC_ERR_UNSUPPORTED for both values.
 * Returns RC_OK, RC_ERR_UNSUPPORTED or RC_ERR. */
int  rc_wallpaper_hide(int hide);

/* ---- session UI (native so it cannot be hidden by the controller) ---- */
/* Show/hide the always-on-top red border on every monitor. */
void rc_indicator_show(int on);
/* Blocking consent prompt on the device screen.
 * Returns 0 = deny/timeout, 1 = allow once, 2 = allow and remember.
 * Backends without a native UI (linux docker) return RC_ERR_UNSUPPORTED and
 * the Go side uses another prompter. */
int  rc_consent_prompt(const char *title, const char *message, int32_t timeout_s);

#ifdef __cplusplus
}
#endif

#endif /* RC_NATIVE_H */
