// Internal declarations shared by the Windows backend translation units.
// Win32 threads and locks only (no std::thread/std::mutex) so the code builds
// with both MSVC and mingw-w64's win32 thread model.
#pragma once

#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#ifndef _WIN32_WINNT
#define _WIN32_WINNT 0x0A00
#endif

#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <stdint.h>
#include <string.h>

#include "../../include/rc_native.h"

namespace rcw {

// ---- minimal COM smart pointer (avoids a WRL dependency) ----
template <class T>
class Com {
public:
    Com() : p_(nullptr) {}
    Com(const Com &o) : p_(o.p_) { if (p_) p_->AddRef(); }
    ~Com() { reset(); }
    Com &operator=(const Com &o) {
        if (this != &o) { reset(); p_ = o.p_; if (p_) p_->AddRef(); }
        return *this;
    }
    void reset() { if (p_) { p_->Release(); p_ = nullptr; } }
    T *get() const { return p_; }
    T *operator->() const { return p_; }
    explicit operator bool() const { return p_ != nullptr; }
    T **put() { reset(); return &p_; }
    void **put_void() { reset(); return reinterpret_cast<void **>(&p_); }
    void attach(T *p) { reset(); p_ = p; }
    template <class U>
    HRESULT as(Com<U> &out) const {
        if (!p_) return E_POINTER;
        return p_->QueryInterface(__uuidof(U), out.put_void());
    }
private:
    T *p_;
};

// ---- locks ----
class Lock {
public:
    Lock() { InitializeSRWLock(&l_); }
    void lock() { AcquireSRWLockExclusive(&l_); }
    void unlock() { ReleaseSRWLockExclusive(&l_); }
private:
    SRWLOCK l_;
};
class Guard {
public:
    explicit Guard(Lock &l) : l_(l) { l_.lock(); }
    ~Guard() { l_.unlock(); }
private:
    Lock &l_;
};

// ---- logging (OutputDebugString + stderr when RC_NATIVE_DEBUG is set) ----
void logf(const char *fmt, ...);

// ---- strings ----
// Returns a malloc'd UTF-16 string; caller frees with free().
wchar_t *utf8_to_wide(const char *s, int32_t len);
// Writes UTF-8 into out (NUL-terminated, truncated to cap).
void wide_to_utf8(const wchar_t *w, char *out, int cap);

// ---- time ----
int64_t now_us();

// ---- desktops ----
// Attaches the calling thread to the current input desktop (Default,
// Winlogon for UAC / lock screen). Cheap when nothing changed; safe to call
// often. Returns true when the thread is on the input desktop.
bool attach_input_desktop();
// True when the input desktop is the normal user desktop ("Default").
bool input_desktop_is_default();

// ---- monitors ----
struct MonitorInfo {
    rc_monitor pub;
    RECT rect;          // desktop coordinates, physical pixels
    HMONITOR hmon;
    UINT adapter_index;
    UINT output_index;
};
// Re-enumerates DXGI outputs and refreshes the cache used by input mapping.
int enumerate_monitors(MonitorInfo *out, int max);
// Looks up a monitor by id in the cache (enumerating once if empty).
bool find_monitor(uint32_t id, MonitorInfo *out);
// Virtual desktop rect (all monitors).
RECT virtual_desktop();

// ---- Media Foundation, loaded at runtime (mfapi_dyn.cpp) ----
struct MfApi {
    bool ok = false;
    HRESULT(WINAPI *Startup)(ULONG, DWORD) = nullptr;
    HRESULT(WINAPI *Shutdown)() = nullptr;
    HRESULT(WINAPI *CreateMediaType)(IMFMediaType **) = nullptr;
    HRESULT(WINAPI *CreateSample)(IMFSample **) = nullptr;
    HRESULT(WINAPI *CreateMemoryBuffer)(DWORD, IMFMediaBuffer **) = nullptr;
    HRESULT(WINAPI *CreateDXGISurfaceBuffer)(REFIID, IUnknown *, UINT, BOOL, IMFMediaBuffer **) = nullptr;
    HRESULT(WINAPI *CreateDXGIDeviceManager)(UINT *, IMFDXGIDeviceManager **) = nullptr;
    HRESULT(WINAPI *TEnumEx)(GUID, UINT32, const MFT_REGISTER_TYPE_INFO *, const MFT_REGISTER_TYPE_INFO *,
                             IMFActivate ***, UINT32 *) = nullptr;
};
const MfApi &mf();
bool mf_startup();   // false when Media Foundation is unavailable
void mf_shutdown();

// ---- encoder ----
struct EncoderConfig {
    UINT width, height;
    int fps;
    int bitrate_kbps;
    int keyframe_interval_ms;
    int profile_idc; // negotiated H.264 profile: the most the encoder may use
};
class Encoder;
// Creates an encoder on dev. prefer_hw=false skips hardware MFTs.
Encoder *encoder_create(ID3D11Device *dev, const EncoderConfig &cfg, bool prefer_hw,
                        rc_frame_fn on_frame, void *user);
void encoder_destroy(Encoder *e);
// Submits one NV12 texture (owned by the caller's ring; must stay untouched
// until a later call reuses it). Returns false when the frame was dropped
// (encoder busy) and true when it was accepted. *fatal is set on errors that
// require recreating the encoder.
bool encoder_encode(Encoder *e, ID3D11Texture2D *nv12, int64_t pts_us, bool force_key, bool *fatal);
void encoder_set_bitrate(Encoder *e, int kbps);
const char *encoder_name(Encoder *e);
// Probes MF for an H.264 hardware MFT; "mf-hw" or "mf-sw".
const char *encoder_probe();

// ---- capture (capture.cpp) ----
int capture_start(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                  rc_cursor_pos_fn on_pos, void *user);
void capture_stop();
void capture_configure(int32_t bitrate_kbps, int32_t fps);
void capture_request_keyframe();
const char *capture_encoder_name(); // nullptr when not capturing

// ---- input + clipboard (input.cpp) ----
int input_inject(const rc_input_event *ev);
int input_text(const char *utf8, int32_t len);
int clipboard_get_text(char **out, int32_t *len);
int clipboard_set_text(const char *utf8, int32_t len);

// ---- cursor ----
// Starts/stops the cursor polling thread for a monitor rect.
void cursor_start(RECT monitor_rect, rc_cursor_shape_fn on_shape, rc_cursor_pos_fn on_pos, void *user);
void cursor_stop();

// ---- ui ----
void indicator_show(bool on);

// ---- wallpaper.cpp ----
int wallpaper_hide(bool hide);
// Shows a wallpaper an earlier, crashed helper left hidden (rc_init).
void wallpaper_recover();
int consent_prompt(const char *title, const char *message, int timeout_s);

} // namespace rcw
