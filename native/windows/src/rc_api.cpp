// extern "C" entry points of rc_native.h for Windows.
#include "rc_win.h"

#include <stdlib.h>

using namespace rcw;

typedef BOOL(WINAPI *SetDpiCtxFn)(DPI_AWARENESS_CONTEXT);

static const char *g_probed_encoder = "mf";

extern "C" {

int rc_init(void) {
    // Physical-pixel coordinates everywhere (capture, cursor, SendInput).
    HMODULE user32 = GetModuleHandleW(L"user32.dll");
    SetDpiCtxFn set_ctx = user32 ? (SetDpiCtxFn)(void *)GetProcAddress(user32, "SetProcessDpiAwarenessContext") : nullptr;
    if (!set_ctx || !set_ctx(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)) SetProcessDPIAware();
    g_probed_encoder = encoder_probe();
    enumerate_monitors(nullptr, 0);
    wallpaper_recover();
    return RC_OK;
}

void rc_shutdown(void) {
    capture_stop();
    indicator_show(false);
    wallpaper_hide(false);
}

const char *rc_backend_name(void) { return "windows"; }

int rc_list_monitors(rc_monitor *out, int max) {
    MonitorInfo infos[16];
    int n = enumerate_monitors(infos, 16);
    if (n < 0) return n;
    int w = n < max ? n : max;
    for (int i = 0; i < w; i++) out[i] = infos[i].pub;
    return w;
}

int rc_check_permissions(int *screen, int *input) {
    // No TCC equivalent: the helper runs as SYSTEM in the console session.
    if (screen) *screen = 1;
    if (input) *input = 1;
    return RC_OK;
}

void rc_request_permission(int) {}

int rc_capture_start(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                     rc_cursor_pos_fn on_pos, void *user) {
    return capture_start(cfg, on_frame, on_shape, on_pos, user);
}

void rc_capture_stop(void) { capture_stop(); }

void rc_encode_configure(int32_t bitrate_kbps, int32_t fps) { capture_configure(bitrate_kbps, fps); }

void rc_request_keyframe(void) { capture_request_keyframe(); }

const char *rc_encoder_name(void) {
    const char *n = capture_encoder_name();
    return n ? n : g_probed_encoder;
}

int rc_input_inject(const rc_input_event *ev) { return input_inject(ev); }

int rc_input_text(const char *utf8, int32_t len) { return input_text(utf8, len); }

int rc_clipboard_get_text(char **out, int32_t *len) { return clipboard_get_text(out, len); }

int rc_clipboard_set_text(const char *utf8, int32_t len) { return clipboard_set_text(utf8, len); }

void rc_free(void *p) { free(p); }

void rc_indicator_show(int on) { indicator_show(on != 0); }

int rc_wallpaper_hide(int hide) { return wallpaper_hide(hide != 0); }

int rc_consent_prompt(const char *title, const char *message, int32_t timeout_s) {
    return consent_prompt(title, message, timeout_s);
}

} // extern "C"
