// Capture thread: grabs desktop frames (Windows.Graphics.Capture on the user
// desktop when built with RC_HAVE_WGC, DXGI Desktop Duplication otherwise and
// always on secure desktops), converts BGRA → NV12 with the D3D11 video
// processor, and feeds the MF encoder. Full color (RC_CODEC_VP9) scales to
// BGRA instead, reads it back and encodes with rc_vpx on this thread.
// Before Windows 8 (no Desktop Duplication, no video processor) frames come
// from GDI into system memory, are converted on the CPU and go to the
// software MFT (run_loop_cpu). One capture at a time.
#include "rc_win.h"
#include "rc_scale.h"

#ifdef RC_HAVE_VPX
#include "rc_vpx.h"
#endif
#include "rc_nv12.h"

#include <d3d10.h>
#include <dxgi1_5.h>
#include <mfapi.h>
#include <mmsystem.h>

#include <stdlib.h>
#include <vector>

#ifdef RC_HAVE_WGC
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Graphics.Capture.h>
#include <winrt/Windows.Graphics.DirectX.h>
#include <winrt/Windows.Graphics.DirectX.Direct3D11.h>
#include <windows.graphics.capture.interop.h>
#include <windows.graphics.directx.direct3d11.interop.h>
#endif

namespace rcw {

// ---------------------------------------------------------------- sources

class Source {
public:
    virtual ~Source() {}
    // S_OK with *frame (AddRef'd) when a frame is available; S_FALSE on
    // timeout; a failure HRESULT when the source must be re-opened.
    // *fresh=false means only the pointer moved (no new pixels). *dirty is
    // the fraction of the frame that changed, -1 when the source cannot tell.
    virtual HRESULT next(UINT timeout_ms, ID3D11Texture2D **frame, bool *fresh, float *dirty) = 0;
    // Releases the frame returned by next().
    virtual void done() = 0;
    virtual const char *kind() const = 0;
};

class DxgiSource : public Source {
public:
    DxgiSource(ID3D11Device *dev, IDXGIOutput1 *out) : dev_(dev), out_(out) { out_->AddRef(); }
    ~DxgiSource() override {
        done();
        dup_.reset();
        out_->Release();
    }

    HRESULT next(UINT timeout_ms, ID3D11Texture2D **frame, bool *fresh, float *dirty) override {
        *frame = nullptr;
        *fresh = false;
        *dirty = -1;
        done();
        if (!dup_) {
            HRESULT hr = open();
            if (FAILED(hr)) return hr;
        }
        DXGI_OUTDUPL_FRAME_INFO info;
        Com<IDXGIResource> res;
        HRESULT hr = dup_->AcquireNextFrame(timeout_ms, &info, res.put());
        if (hr == DXGI_ERROR_WAIT_TIMEOUT) return S_FALSE;
        if (FAILED(hr)) {
            // ACCESS_LOST: desktop switch (UAC, lock), mode change, fullscreen app.
            dup_.reset();
            return hr;
        }
        holding_ = true;
        // The first frame after (re)creating the duplication carries the whole
        // desktop but may report LastPresentTime == 0 on a static screen.
        *fresh = first_ || info.LastPresentTime.QuadPart != 0;
        if (*fresh && !first_) *dirty = changed_fraction(info.TotalMetadataBufferSize);
        first_ = false;
        return res->QueryInterface(__uuidof(ID3D11Texture2D), (void **)frame);
    }

    // Dirty and move-destination rects over the output size; -1 unknown.
    float changed_fraction(UINT meta_bytes) {
        DXGI_OUTDUPL_DESC dd;
        dup_->GetDesc(&dd);
        double total = (double)dd.ModeDesc.Width * dd.ModeDesc.Height;
        if (meta_bytes == 0 || total <= 0) return -1;
        if (meta_.size() < meta_bytes) meta_.resize(meta_bytes);
        double area = 0;
        UINT got = 0;
        if (SUCCEEDED(dup_->GetFrameMoveRects(meta_bytes, (DXGI_OUTDUPL_MOVE_RECT *)meta_.data(), &got))) {
            const DXGI_OUTDUPL_MOVE_RECT *m = (const DXGI_OUTDUPL_MOVE_RECT *)meta_.data();
            for (UINT i = 0; i < got / sizeof(*m); i++)
                area += (double)(m[i].DestinationRect.right - m[i].DestinationRect.left) *
                        (m[i].DestinationRect.bottom - m[i].DestinationRect.top);
        } else {
            return -1;
        }
        if (SUCCEEDED(dup_->GetFrameDirtyRects(meta_bytes, (RECT *)meta_.data(), &got))) {
            const RECT *r = (const RECT *)meta_.data();
            for (UINT i = 0; i < got / sizeof(*r); i++) area += (double)(r[i].right - r[i].left) * (r[i].bottom - r[i].top);
        } else {
            return -1;
        }
        return (float)(area < total ? area / total : 1.0);
    }

    void done() override {
        if (holding_ && dup_) dup_->ReleaseFrame();
        holding_ = false;
    }

    const char *kind() const override { return "dxgi"; }

private:
    HRESULT open() {
        attach_input_desktop();
        Com<IDXGIOutput5> out5;
        HRESULT hr = E_NOINTERFACE;
        if (SUCCEEDED(out_->QueryInterface(__uuidof(IDXGIOutput5), out5.put_void()))) {
            DXGI_FORMAT fmts[] = {DXGI_FORMAT_B8G8R8A8_UNORM};
            hr = out5->DuplicateOutput1(dev_, 0, 1, fmts, dup_.put());
        }
        if (FAILED(hr)) hr = out_->DuplicateOutput(dev_, dup_.put());
        if (FAILED(hr)) {
            dup_.reset();
            logf("capture: DuplicateOutput failed 0x%08lx", (unsigned long)hr);
        } else {
            first_ = true;
        }
        return hr;
    }

    ID3D11Device *dev_;
    IDXGIOutput1 *out_;
    Com<IDXGIOutputDuplication> dup_;
    bool holding_ = false;
    bool first_ = false;
    std::vector<uint8_t> meta_; // move + dirty rects of the held frame
};

#ifdef RC_HAVE_WGC
namespace wgc = winrt::Windows::Graphics::Capture;
namespace wdx = winrt::Windows::Graphics::DirectX;

class WgcSource : public Source {
public:
    ~WgcSource() override {
        done();
        try {
            if (session_) session_.Close();
            if (pool_) pool_.Close();
        } catch (...) {
        }
        if (event_) CloseHandle(event_);
    }

    bool open(ID3D11Device *dev, HMONITOR mon) {
        try {
            if (!wgc::GraphicsCaptureSession::IsSupported()) return false;
            Com<IDXGIDevice> dxgi;
            if (FAILED(dev->QueryInterface(__uuidof(IDXGIDevice), dxgi.put_void()))) return false;
            winrt::com_ptr<::IInspectable> insp;
            if (FAILED(CreateDirect3D11DeviceFromDXGIDevice(dxgi.get(), insp.put()))) return false;
            rtdev_ = insp.as<wdx::Direct3D11::IDirect3DDevice>();
            auto interop = winrt::get_activation_factory<wgc::GraphicsCaptureItem, IGraphicsCaptureItemInterop>();
            if (FAILED(interop->CreateForMonitor(mon, winrt::guid_of<wgc::GraphicsCaptureItem>(),
                                                 winrt::put_abi(item_))))
                return false;
            size_ = item_.Size();
            pool_ = wgc::Direct3D11CaptureFramePool::CreateFreeThreaded(
                rtdev_, wdx::DirectXPixelFormat::B8G8R8A8UIntNormalized, 2, size_);
            session_ = pool_.CreateCaptureSession(item_);
            session_.IsCursorCaptureEnabled(false);
            try {
                session_.IsBorderRequired(false);
            } catch (...) {
            }
            event_ = CreateEventW(nullptr, FALSE, FALSE, nullptr);
            HANDLE ev = event_;
            pool_.FrameArrived([ev](auto &&, auto &&) { SetEvent(ev); });
            session_.StartCapture();
            return true;
        } catch (...) {
            return false;
        }
    }

    HRESULT next(UINT timeout_ms, ID3D11Texture2D **frame, bool *fresh, float *dirty) override {
        *frame = nullptr;
        *fresh = false;
        *dirty = -1;
        done();
        if (WaitForSingleObject(event_, timeout_ms) == WAIT_TIMEOUT) return S_FALSE;
        try {
            wgc::Direct3D11CaptureFrame f{nullptr};
            // Drain to the newest frame.
            for (auto n = pool_.TryGetNextFrame(); n; n = pool_.TryGetNextFrame()) {
                if (f) f.Close();
                f = n;
            }
            if (!f) return S_FALSE;
            auto cs = f.ContentSize();
            if (cs.Width != size_.Width || cs.Height != size_.Height) {
                size_ = cs;
                pool_.Recreate(rtdev_, wdx::DirectXPixelFormat::B8G8R8A8UIntNormalized, 2, size_);
            }
            auto access = f.Surface().as<::Windows::Graphics::DirectX::Direct3D11::IDirect3DDxgiInterfaceAccess>();
            HRESULT hr = access->GetInterface(__uuidof(ID3D11Texture2D), (void **)frame);
            if (FAILED(hr)) return hr;
            frame_ = f;
            *fresh = true;
            return S_OK;
        } catch (winrt::hresult_error const &e) {
            return e.code();
        }
    }

    void done() override {
        if (frame_) {
            try {
                frame_.Close();
            } catch (...) {
            }
            frame_ = nullptr;
        }
    }

    const char *kind() const override { return "wgc"; }

private:
    wdx::Direct3D11::IDirect3DDevice rtdev_{nullptr};
    wgc::GraphicsCaptureItem item_{nullptr};
    wgc::Direct3D11CaptureFramePool pool_{nullptr};
    wgc::GraphicsCaptureSession session_{nullptr};
    wgc::Direct3D11CaptureFrame frame_{nullptr};
    winrt::Windows::Graphics::SizeInt32 size_{};
    HANDLE event_ = nullptr;
};
#endif

// ---------------------------------------------------------------- GDI

// A screen copy in system memory for the CPU pipeline: BGRA at the encoded
// size, refreshed by grab().
class CpuSource {
public:
    virtual ~CpuSource() {}
    // Refreshes the copy. *changed is the fraction of the frame that differs
    // from the previous copy (1 for the first). False when the copy failed.
    virtual bool grab(float *changed) = 0;
    virtual bool have() const = 0;
    virtual const uint8_t *pixels() const = 0;
    virtual int stride() const = 0;
    virtual const char *kind() const = 0;
    // True when the source stopped working for good and must be rebuilt on
    // a fresh device and output (the session moved to another display).
    virtual bool stale(int64_t) const { return false; }
};

// Copies (and scales) the monitor into a 32-bit DIB on every grab. GDI reports
// no damage, so each copy is compared with the previous one row by row.
class GdiGrabber : public CpuSource {
public:
    ~GdiGrabber() override { close(); }

    bool open(const RECT &src, UINT w, UINT h) {
        close();
        src_ = src;
        w_ = w;
        h_ = h;
        mem_ = CreateCompatibleDC(nullptr);
        if (!mem_) return false;
        BITMAPINFO bi = {};
        bi.bmiHeader.biSize = sizeof(bi.bmiHeader);
        bi.bmiHeader.biWidth = (LONG)w;
        bi.bmiHeader.biHeight = -(LONG)h; // top-down
        bi.bmiHeader.biPlanes = 1;
        bi.bmiHeader.biBitCount = 32;
        bi.bmiHeader.biCompression = BI_RGB;
        for (int i = 0; i < 2; i++) {
            bmp_[i] = CreateDIBSection(nullptr, &bi, DIB_RGB_COLORS, &bits_[i], nullptr, 0);
            if (!bmp_[i]) {
                logf("capture: CreateDIBSection %ux%u failed", w, h);
                close();
                return false;
            }
        }
        return true;
    }

    bool grab(float *changed) override {
        *changed = 0;
        attach_input_desktop(); // UAC and the lock screen are other desktops
        HDC screen = GetDC(nullptr);
        if (!screen) return false;
        int next = have_ ? cur_ ^ 1 : cur_;
        HGDIOBJ old = SelectObject(mem_, bmp_[next]);
        int sw = src_.right - src_.left, sh = src_.bottom - src_.top;
        BOOL ok;
        if (sw == (int)w_ && sh == (int)h_) {
            ok = BitBlt(mem_, 0, 0, (int)w_, (int)h_, screen, src_.left, src_.top, SRCCOPY);
        } else {
            SetStretchBltMode(mem_, HALFTONE);
            SetBrushOrgEx(mem_, 0, 0, nullptr);
            ok = StretchBlt(mem_, 0, 0, (int)w_, (int)h_, screen, src_.left, src_.top, sw, sh, SRCCOPY);
        }
        SelectObject(mem_, old);
        ReleaseDC(nullptr, screen);
        if (!ok) return false;
        GdiFlush();
        if (!have_) {
            have_ = true;
            *changed = 1;
            return true;
        }
        size_t stride = (size_t)w_ * 4;
        const uint8_t *a = (const uint8_t *)bits_[next], *b = (const uint8_t *)bits_[cur_];
        UINT rows = 0;
        for (UINT y = 0; y < h_; y++)
            if (memcmp(a + y * stride, b + y * stride, stride) != 0) rows++;
        if (rows > 0) cur_ = next;
        *changed = (float)rows / (float)h_;
        return true;
    }

    bool have() const override { return have_; }
    const uint8_t *pixels() const override { return (const uint8_t *)bits_[cur_]; }
    int stride() const override { return (int)w_ * 4; }
    const char *kind() const override { return "gdi"; }

private:
    void close() {
        for (int i = 0; i < 2; i++) {
            if (bmp_[i]) DeleteObject(bmp_[i]);
            bmp_[i] = nullptr;
            bits_[i] = nullptr;
        }
        if (mem_) DeleteDC(mem_);
        mem_ = nullptr;
        have_ = false;
        cur_ = 0;
    }

    RECT src_{};
    UINT w_ = 0, h_ = 0;
    HDC mem_ = nullptr;
    HBITMAP bmp_[2] = {};
    void *bits_[2] = {};
    int cur_ = 0; // the newest copy
    bool have_ = false;
};

// Desktop Duplication read back to system memory: for a device that has no
// video processor or GPU encoder (a server's display-only adapter). Sees what
// GDI cannot (the sign-in screen, DirectComposition windows).
class DxgiReadback : public CpuSource {
public:
    DxgiReadback(ID3D11Device *dev, ID3D11DeviceContext *ctx, IDXGIOutput1 *out, UINT w, UINT h)
        : src_(dev, out), dev_(dev), ctx_(ctx), w_(w), h_(h), buf_((size_t)w * h * 4) {}

    bool grab(float *changed) override {
        *changed = 0;
        ID3D11Texture2D *frame = nullptr;
        bool fresh = false;
        float part = -1;
        HRESULT hr = src_.next(0, &frame, &fresh, &part);
        if (hr == S_FALSE) return true; // nothing new
        if (FAILED(hr) || !frame) {
            if (hr != last_hr_) logf("capture: duplication failed 0x%08lx", (unsigned long)hr);
            last_hr_ = hr;
            fails_++;
            last_fail_ = now_us();
            frame_since_fail_ = false;
            return false;
        }
        last_hr_ = S_OK;
        fails_ = 0;
        bool ok = !fresh || copy(frame);
        frame->Release();
        src_.done();
        if (!ok) return false;
        if (fresh) {
            if (!have_) logf("capture: first duplicated frame %ux%u", sw_, sh_);
            *changed = !have_ || part < 0 ? 1 : part;
            have_ = true;
            frame_since_fail_ = true;
        }
        return true;
    }

    // Failing every time, or silent since a failure: a re-created
    // duplication delivers the whole desktop at once, so silence means the
    // output it was made for no longer shows this session.
    bool stale(int64_t now) const override {
        return fails_ >= 20 || (last_fail_ && !frame_since_fail_ && now - last_fail_ > 2000000);
    }

    bool have() const override { return have_; }
    const uint8_t *pixels() const override { return buf_.data(); }
    int stride() const override { return (int)w_ * 4; }
    const char *kind() const override { return "dxgi-readback"; }

private:
    bool copy(ID3D11Texture2D *frame) {
        D3D11_TEXTURE2D_DESC d;
        frame->GetDesc(&d);
        if (d.Format != DXGI_FORMAT_B8G8R8A8_UNORM) {
            logf("capture: duplication format %d is not BGRA", (int)d.Format);
            return false;
        }
        if (!staging_ || d.Width != sw_ || d.Height != sh_) {
            D3D11_TEXTURE2D_DESC sd = {};
            sd.Width = d.Width;
            sd.Height = d.Height;
            sd.MipLevels = 1;
            sd.ArraySize = 1;
            sd.Format = d.Format;
            sd.SampleDesc.Count = 1;
            sd.Usage = D3D11_USAGE_STAGING;
            sd.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
            if (FAILED(dev_->CreateTexture2D(&sd, nullptr, staging_.put()))) {
                logf("capture: staging texture %ux%u failed", d.Width, d.Height);
                return false;
            }
            sw_ = d.Width;
            sh_ = d.Height;
        }
        ctx_->CopyResource(staging_.get(), frame);
        D3D11_MAPPED_SUBRESOURCE m;
        if (FAILED(ctx_->Map(staging_.get(), 0, D3D11_MAP_READ, 0, &m))) return false;
        const uint8_t *px = (const uint8_t *)m.pData;
        size_t row = (size_t)w_ * 4;
        if (sw_ == w_ && sh_ == h_) {
            for (UINT y = 0; y < h_; y++) memcpy(&buf_[y * row], px + (size_t)y * m.RowPitch, row);
        } else if (sw_ >= w_ && sh_ >= h_) {
            rc_scale_bgra(px, (int32_t)sw_, (int32_t)sh_, (int32_t)m.RowPitch, buf_.data(), (int32_t)w_, (int32_t)h_);
        } else {
            // The mode shrank below the encoded size: top-left, black around.
            memset(buf_.data(), 0, buf_.size());
            for (UINT y = 0; y < sh_; y++) memcpy(&buf_[y * row], px + (size_t)y * m.RowPitch, (size_t)sw_ * 4);
        }
        ctx_->Unmap(staging_.get(), 0);
        return true;
    }

    DxgiSource src_;
    ID3D11Device *dev_;
    ID3D11DeviceContext *ctx_;
    UINT w_, h_;
    UINT sw_ = 0, sh_ = 0;
    Com<ID3D11Texture2D> staging_;
    std::vector<uint8_t> buf_;
    bool have_ = false;
    HRESULT last_hr_ = S_OK;
    int fails_ = 0;           // in a row
    int64_t last_fail_ = 0;   // now_us() of the latest failure, 0 none
    bool frame_since_fail_ = true;
};

// ---------------------------------------------------------------- state

static const int kRing = 4;

struct Capture {
    rc_capture_config cfg;
    rc_frame_fn on_frame;
    rc_cursor_shape_fn on_shape;
    rc_cursor_pos_fn on_pos;
    void *user;

    HANDLE thread = nullptr;
    HANDLE started = nullptr;
    int start_result = RC_ERR;
    volatile LONG stop = 0;
    volatile LONG bitrate_kbps = 0;
    volatile LONG fps = 0;
    volatile LONG keyframe_req = 0;
    volatile LONG refine_qp = 0;  // pending rc_encode_refine, 0 = none, -1 = a repeat pass
    volatile LONG refine_ok = 0;  // the current encoder can refine
    const char *volatile encoder = "mf";

    // Thread-owned resources.
    MonitorInfo mon;
    Com<IDXGIOutput1> output;
    Com<ID3D11Device> dev;
    Com<ID3D11DeviceContext> ctx;
    Com<ID3D11VideoDevice> vdev;
    Com<ID3D11VideoContext> vctx;
    Com<ID3D11VideoProcessorEnumerator> venum;
    Com<ID3D11VideoProcessor> vp;
    Com<ID3D11Texture2D> last;          // copy of the most recent desktop frame
    Com<ID3D11VideoProcessorInputView> last_view;
    UINT src_w = 0, src_h = 0;
    DXGI_FORMAT src_fmt = DXGI_FORMAT_UNKNOWN;
    UINT dst_w = 0, dst_h = 0;
    Com<ID3D11Texture2D> ring[kRing];
    Com<ID3D11VideoProcessorOutputView> ring_view[kRing];
    Encoder *enc = nullptr;
    Source *src = nullptr;
    bool full_color = false; // cfg.codec == RC_CODEC_VP9: ring is BGRA, read back through staging
    bool cpu = false;        // a CpuSource, CPU conversion, software encoder (run_loop_cpu)
    CpuSource *grab = nullptr;
    std::vector<uint8_t> nv12; // cpu: the frame handed to the encoder
#ifdef RC_HAVE_VPX
    rc_vpx *vpx = nullptr;
#endif
    Com<ID3D11Texture2D> staging;
};

static Lock g_cap_lock;
static Capture *g_cap = nullptr;

static void target_size(const rc_capture_config &cfg, UINT w, UINT h, UINT *ow, UINT *oh) {
    double s = 1.0;
    if (cfg.max_width > 0 && (int)w > cfg.max_width) s = (double)cfg.max_width / w;
    if (cfg.max_height > 0 && h * s > cfg.max_height) s = (double)cfg.max_height / h;
    *ow = ((UINT)(w * s + 0.5)) & ~1u; // nearest: a bound the agent sized exactly stays exact
    *oh = ((UINT)(h * s + 0.5)) & ~1u;
    if (*ow < 16) *ow = 16;
    if (*oh < 16) *oh = 16;
}

// The device for the monitor's adapter. video: the GPU pipeline (video
// processor, GPU encoder); otherwise any feature level that can duplicate
// the desktop, as display-only adapters offer.
static bool create_device(Capture *c, bool video) {
    Com<IDXGIFactory1> factory;
    if (FAILED(CreateDXGIFactory1(__uuidof(IDXGIFactory1), factory.put_void()))) return false;
    Com<IDXGIAdapter1> adapter;
    if (FAILED(factory->EnumAdapters1(c->mon.adapter_index, adapter.put()))) return false;
    Com<IDXGIOutput> out;
    if (FAILED(adapter->EnumOutputs(c->mon.output_index, out.put()))) return false;
    if (FAILED(out.as(c->output))) return false;

    D3D_FEATURE_LEVEL levels[] = {D3D_FEATURE_LEVEL_11_1, D3D_FEATURE_LEVEL_11_0, D3D_FEATURE_LEVEL_10_1,
                                  D3D_FEATURE_LEVEL_10_0, D3D_FEATURE_LEVEL_9_3,  D3D_FEATURE_LEVEL_9_2,
                                  D3D_FEATURE_LEVEL_9_1};
    UINT nlevels = video ? 2 : (UINT)(sizeof(levels) / sizeof(levels[0]));
    UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT | (video ? D3D11_CREATE_DEVICE_VIDEO_SUPPORT : 0);
    D3D_FEATURE_LEVEL got = D3D_FEATURE_LEVEL_9_1;
    HRESULT hr = D3D11CreateDevice(adapter.get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, flags, levels, nlevels,
                                   D3D11_SDK_VERSION, c->dev.put(), &got, c->ctx.put());
    if (hr == E_INVALIDARG) // no 11_1 before Windows 8.1
        hr = D3D11CreateDevice(adapter.get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, flags, levels + 1, nlevels - 1,
                               D3D11_SDK_VERSION, c->dev.put(), &got, c->ctx.put());
    if (FAILED(hr)) {
        logf("capture: D3D11CreateDevice%s failed 0x%08lx", video ? " (video)" : "", (unsigned long)hr);
        return false;
    }
    if (!video) {
        logf("capture: D3D11 device at feature level 0x%x for readback", (unsigned)got);
        return true;
    }
    // The MF encoder uses the device from its own threads.
    Com<ID3D10Multithread> mt;
    if (SUCCEEDED(c->dev.as(mt))) mt->SetMultithreadProtected(TRUE);
    if (FAILED(c->dev.as(c->vdev)) || FAILED(c->ctx.as(c->vctx))) return false;
    return true;
}

// (Re)creates the BGRA copy target and the video processor for a source size.
static bool setup_processor(Capture *c, UINT w, UINT h, DXGI_FORMAT fmt) {
    c->last_view.reset();
    c->last.reset();
    c->vp.reset();
    c->venum.reset();
    for (int i = 0; i < kRing; i++) c->ring_view[i].reset();

    D3D11_TEXTURE2D_DESC td = {};
    td.Width = w;
    td.Height = h;
    td.MipLevels = 1;
    td.ArraySize = 1;
    td.Format = fmt;
    td.SampleDesc.Count = 1;
    td.Usage = D3D11_USAGE_DEFAULT;
    td.BindFlags = D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE;
    if (FAILED(c->dev->CreateTexture2D(&td, nullptr, c->last.put()))) return false;

    D3D11_VIDEO_PROCESSOR_CONTENT_DESC cd = {};
    cd.InputFrameFormat = D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE;
    cd.InputWidth = w;
    cd.InputHeight = h;
    cd.OutputWidth = c->dst_w;
    cd.OutputHeight = c->dst_h;
    cd.InputFrameRate.Numerator = (UINT)c->cfg.fps;
    cd.InputFrameRate.Denominator = 1;
    cd.OutputFrameRate = cd.InputFrameRate;
    cd.Usage = D3D11_VIDEO_USAGE_OPTIMAL_SPEED;
    if (FAILED(c->vdev->CreateVideoProcessorEnumerator(&cd, c->venum.put()))) return false;
    if (FAILED(c->vdev->CreateVideoProcessor(c->venum.get(), 0, c->vp.put()))) return false;

    D3D11_VIDEO_PROCESSOR_INPUT_VIEW_DESC ivd = {};
    ivd.ViewDimension = D3D11_VPIV_DIMENSION_TEXTURE2D;
    if (FAILED(c->vdev->CreateVideoProcessorInputView(c->last.get(), c->venum.get(), &ivd, c->last_view.put())))
        return false;

    if (!c->ring[0]) {
        D3D11_TEXTURE2D_DESC nd = {};
        nd.Width = c->dst_w;
        nd.Height = c->dst_h;
        nd.MipLevels = 1;
        nd.ArraySize = 1;
        nd.Format = c->full_color ? DXGI_FORMAT_B8G8R8A8_UNORM : DXGI_FORMAT_NV12;
        nd.SampleDesc.Count = 1;
        nd.Usage = D3D11_USAGE_DEFAULT;
        nd.BindFlags = D3D11_BIND_RENDER_TARGET;
        for (int i = 0; i < kRing; i++)
            if (FAILED(c->dev->CreateTexture2D(&nd, nullptr, c->ring[i].put()))) return false;
        if (c->full_color) {
            nd.Usage = D3D11_USAGE_STAGING;
            nd.BindFlags = 0;
            nd.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
            if (FAILED(c->dev->CreateTexture2D(&nd, nullptr, c->staging.put()))) return false;
        }
    }
    D3D11_VIDEO_PROCESSOR_OUTPUT_VIEW_DESC ovd = {};
    ovd.ViewDimension = D3D11_VPOV_DIMENSION_TEXTURE2D;
    for (int i = 0; i < kRing; i++)
        if (FAILED(c->vdev->CreateVideoProcessorOutputView(c->ring[i].get(), c->venum.get(), &ovd,
                                                           c->ring_view[i].put())))
            return false;

    D3D11_VIDEO_PROCESSOR_COLOR_SPACE in = {};
    in.RGB_Range = 0; // full range RGB desktop
    c->vctx->VideoProcessorSetStreamColorSpace(c->vp.get(), 0, &in);
    D3D11_VIDEO_PROCESSOR_COLOR_SPACE out = {};
    if (c->full_color) {
        out.RGB_Range = 0; // a plain scaler: full range BGRA in and out
    } else {
        out.YCbCr_Matrix = 1; // BT.709
        out.Nominal_Range = D3D11_VIDEO_PROCESSOR_NOMINAL_RANGE_16_235;
    }
    c->vctx->VideoProcessorSetOutputColorSpace(c->vp.get(), &out);
    c->vctx->VideoProcessorSetStreamFrameFormat(c->vp.get(), 0, D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE);
    c->vctx->VideoProcessorSetStreamAutoProcessingMode(c->vp.get(), 0, FALSE);

    c->src_w = w;
    c->src_h = h;
    c->src_fmt = fmt;
    return true;
}

static bool convert(Capture *c, int idx) {
    D3D11_VIDEO_PROCESSOR_STREAM st = {};
    st.Enable = TRUE;
    st.pInputSurface = c->last_view.get();
    return SUCCEEDED(c->vctx->VideoProcessorBlt(c->vp.get(), c->ring_view[idx].get(), 0, 1, &st));
}

#ifdef RC_HAVE_VPX
// Reads ring[idx] back and encodes it as full color; false when the frame
// could not be read.
static bool encode_full_color(Capture *c, int idx, int64_t now, bool key, int max_qp, int32_t flags) {
    c->ctx->CopyResource(c->staging.get(), c->ring[idx].get());
    D3D11_MAPPED_SUBRESOURCE m;
    if (FAILED(c->ctx->Map(c->staging.get(), 0, D3D11_MAP_READ, 0, &m))) return false;
    rc_vpx_from_bgra(c->vpx, (const uint8_t *)m.pData, (int)m.RowPitch);
    c->ctx->Unmap(c->staging.get(), 0);
    return rc_vpx_encode(c->vpx, now, key, max_qp, flags, c->on_frame, c->user) == RC_OK;
}
#endif

// (Re)creates the H.264 encoder at the current rate; false when none is left.
static bool make_encoder(Capture *c, bool prefer_hw) {
    if (c->enc) encoder_destroy(c->enc);
    EncoderConfig ec = {c->dst_w, c->dst_h, (int)c->fps, (int)c->bitrate_kbps, c->cfg.keyframe_interval_ms,
                        c->cfg.profile_idc};
    c->enc = encoder_create(c->cpu ? nullptr : c->dev.get(), ec, prefer_hw && !c->cpu, c->on_frame, c->user);
    if (!c->enc) return false;
    c->encoder = encoder_name(c->enc);
    InterlockedExchange(&c->refine_ok, encoder_can_refine(c->enc) ? 1 : 0);
    return true;
}

static Source *open_source(Capture *c) {
#ifdef RC_HAVE_WGC
    if (input_desktop_is_default()) {
        WgcSource *w = new WgcSource();
        if (w->open(c->dev.get(), c->mon.hmon)) return w;
        delete w;
    }
#endif
    return new DxgiSource(c->dev.get(), c->output.get());
}

// How a capture loop ended.
enum LoopEnd {
    kLoopStopped, // capture_stop, or no encoder left
    kLoopBlind,   // the source never delivered a frame
    kLoopStale,   // the source worked, then stopped (rebuild it)
    kLoopBlack,   // the source shows black where GDI sees the desktop (use GDI)
};

// Desktop Duplication on a server without a GPU (Windows Server 2019 on a
// display-only adapter) can deliver black, or nothing, while GDI copies the
// session's desktop, as the previews do. Checked while the source looks dark.
static const int64_t kProbeFirstUs = 1000000, kProbeUs = 2000000;

// The black fraction of a small GDI copy of the monitor.
static float gdi_dark_fraction(const RECT &r) {
    const int w = 32, h = 32;
    attach_input_desktop();
    HDC screen = GetDC(nullptr);
    if (!screen) return 1;
    HDC mem = CreateCompatibleDC(screen);
    BITMAPINFO bi = {};
    bi.bmiHeader.biSize = sizeof(bi.bmiHeader);
    bi.bmiHeader.biWidth = w;
    bi.bmiHeader.biHeight = -h;
    bi.bmiHeader.biPlanes = 1;
    bi.bmiHeader.biBitCount = 32;
    bi.bmiHeader.biCompression = BI_RGB;
    void *bits = nullptr;
    HBITMAP bmp = mem ? CreateDIBSection(screen, &bi, DIB_RGB_COLORS, &bits, nullptr, 0) : nullptr;
    float dark = 1;
    if (bmp) {
        HGDIOBJ old = SelectObject(mem, bmp);
        SetStretchBltMode(mem, COLORONCOLOR); // samples, as rc_dark_fraction does
        BOOL ok = StretchBlt(mem, 0, 0, w, h, screen, r.left, r.top, r.right - r.left, r.bottom - r.top, SRCCOPY);
        SelectObject(mem, old);
        GdiFlush();
        if (ok) dark = rc_dark_fraction((const uint8_t *)bits, w, h, w * 4);
        DeleteObject(bmp);
    }
    if (mem) DeleteDC(mem);
    ReleaseDC(nullptr, screen);
    return dark;
}

// True when the captured frame is black (fraction dup; the "Activate Windows"
// watermark may show on it) and GDI sees more of a picture. The first probe of
// a loop is logged.
static bool blacked_out(Capture *c, float dup, int *probes) {
    if (dup <= 0.9f) return false;
    float gdi = gdi_dark_fraction(c->mon.rect);
    if ((*probes)++ == 0) logf("capture: black probe: captured %.2f, gdi %.2f", dup, gdi);
    return gdi < dup - 0.1f;
}

// The GPU pipeline reads its frame back only this long after it starts.
static const int64_t kProbeForUs = 10000000;

// The black fraction of the GPU pipeline's latest frame, 1 with none yet.
static float frame_dark(Capture *c, bool have, Com<ID3D11Texture2D> &probe) {
    if (!have) return 1;
    if (c->src_fmt != DXGI_FORMAT_B8G8R8A8_UNORM) return 0;
    D3D11_TEXTURE2D_DESC d;
    if (probe) probe->GetDesc(&d);
    if (!probe || d.Width != c->src_w || d.Height != c->src_h) {
        probe.reset();
        D3D11_TEXTURE2D_DESC sd = {};
        sd.Width = c->src_w;
        sd.Height = c->src_h;
        sd.MipLevels = 1;
        sd.ArraySize = 1;
        sd.Format = c->src_fmt;
        sd.SampleDesc.Count = 1;
        sd.Usage = D3D11_USAGE_STAGING;
        sd.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
        if (FAILED(c->dev->CreateTexture2D(&sd, nullptr, probe.put()))) return 0;
    }
    c->ctx->CopyResource(probe.get(), c->last.get());
    D3D11_MAPPED_SUBRESOURCE m;
    if (FAILED(c->ctx->Map(probe.get(), 0, D3D11_MAP_READ, 0, &m))) return 0;
    float dark = rc_dark_fraction((const uint8_t *)m.pData, (int32_t)c->src_w, (int32_t)c->src_h, (int32_t)m.RowPitch);
    c->ctx->Unmap(probe.get(), 0);
    return dark;
}

// How long Desktop Duplication may fail before its first frame until the
// GDI pipeline takes over (a display-only adapter on a server never works).
static const int64_t kGpuGraceUs = 3000000;

// Runs the D3D11 pipeline until stopped, its source kept failing and never
// delivered a frame, or it shows black where GDI sees the desktop.
static LoopEnd run_loop(Capture *c) {
    int64_t started = now_us();
    int64_t interval = 1000000 / (c->fps > 0 ? c->fps : 30);
    int64_t last_encode = 0, last_content = 0;
    int64_t last_desktop_check = now_us();
    LONG applied_bitrate = c->bitrate_kbps, applied_fps = c->fps;
    bool dirty = false, have = false, sent_any = false;
    float changed = 0; // fraction of the frame changed since the last encode, -1 unknown
    int idx = 0;
    int failures = 0;
    int64_t next_probe = started + kProbeFirstUs;
    int probes = 0;
    Com<ID3D11Texture2D> probe;

    while (!c->stop) {
        LONG br = c->bitrate_kbps;
        LONG f = c->fps;
        if (br != applied_bitrate && c->enc) {
            encoder_set_bitrate(c->enc, br);
            applied_bitrate = br;
        }
#ifdef RC_HAVE_VPX
        if (c->vpx && (br != applied_bitrate || f != applied_fps)) {
            rc_vpx_rate(c->vpx, br, f);
            applied_bitrate = br;
            applied_fps = f;
        }
#endif
        interval = 1000000 / (f > 0 ? f : 30);

        int64_t now = now_us();
        int64_t wait_us = dirty ? (last_encode + interval - now) : (interval < 20000 ? interval : 20000);
        if (wait_us < 0) wait_us = 0;

        ID3D11Texture2D *frame = nullptr;
        bool fresh = false;
        float part = -1;
        HRESULT hr = c->src->next((UINT)(wait_us / 1000), &frame, &fresh, &part);
        if (hr == S_OK && frame) {
            failures = 0;
            if (fresh) {
                D3D11_TEXTURE2D_DESC d;
                frame->GetDesc(&d);
                if (d.Width != c->src_w || d.Height != c->src_h || d.Format != c->src_fmt) {
                    if (!setup_processor(c, d.Width, d.Height, d.Format)) {
                        logf("capture: video processor setup failed for %ux%u", d.Width, d.Height);
                        frame->Release();
                        c->src->done();
                        Sleep(100);
                        continue;
                    }
                }
                c->ctx->CopyResource(c->last.get(), frame);
                changed = !dirty ? part : (changed < 0 || part < 0) ? -1 : changed + part;
                dirty = true;
                have = true;
                last_content = now_us();
            }
            frame->Release();
            c->src->done();
        } else if (FAILED(hr)) {
            // Re-open on the next iteration; back off a little on repeated failure.
            if (++failures > 3) Sleep(50);
            if (!have && failures > 3 && now_us() - started > kGpuGraceUs) {
                logf("capture: %s source failing (0x%08lx) with no frame yet", c->src->kind(), (unsigned long)hr);
                return kLoopBlind;
            }
        }

        // Follow the input desktop: WGC cannot see UAC / lock screen.
        now = now_us();
        if (now - last_desktop_check > 500000) {
            last_desktop_check = now;
#ifdef RC_HAVE_WGC
            bool def = input_desktop_is_default();
            bool is_wgc = strcmp(c->src->kind(), "wgc") == 0;
            if (def != is_wgc) {
                delete c->src;
                c->src = open_source(c);
                InterlockedExchange(&c->keyframe_req, 1);
            }
#endif
        }
        if (now >= next_probe && now - started < kProbeForUs) {
            next_probe = now + kProbeUs;
            if (blacked_out(c, frame_dark(c, have, probe), &probes)) {
                logf("capture: %s shows %s where GDI sees the desktop", c->src->kind(), have ? "black" : "nothing");
                return kLoopBlack;
            }
        }

        bool key = InterlockedExchange(&c->keyframe_req, 0) != 0;
        if (key && have) dirty = true;
        // A refinement pass re-encodes `last` while nothing new arrived.
        LONG refine = InterlockedExchange(&c->refine_qp, 0);
        if (dirty || !sent_any || now - last_content < 100000) refine = 0;
        if ((!dirty && !refine) || !have || now - last_encode < interval - 1000) {
            if (key) InterlockedExchange(&c->keyframe_req, 1);
            if (refine) InterlockedCompareExchange(&c->refine_qp, refine, 0);
            continue;
        }
        if (!convert(c, idx)) {
            logf("capture: VideoProcessorBlt failed");
            if (key) InterlockedExchange(&c->keyframe_req, 1);
            continue;
        }
        bool fatal = false;
        bool accepted;
        int32_t flags = refine ? RC_FRAME_STATIC : (dirty && changed >= 0 && changed <= 0.02f) ? RC_FRAME_MINOR : 0;
#ifdef RC_HAVE_VPX
        if (c->vpx)
            accepted = encode_full_color(c, idx, now, key || !sent_any, refine > 0 ? (int)refine : 0, flags);
        else
#endif
            accepted = encoder_encode(c->enc, c->ring[idx].get(), now, key || !sent_any, &fatal,
                                      refine > 0 ? (int)refine : 0, flags);
        if (accepted) {
            idx = (idx + 1) % kRing;
            last_encode = now;
            dirty = false;
            sent_any = true;
        } else if (key || !sent_any) {
            InterlockedExchange(&c->keyframe_req, 1);
        }
        if (fatal) {
            // Hardware encoders can fail mid-session (driver reset); fall back to software.
            logf("capture: encoder failed, recreating with the software MFT");
            if (!make_encoder(c, false)) {
                logf("capture: no encoder available, stopping");
                return kLoopStopped;
            }
            sent_any = false;
        }
    }
    return kLoopStopped;
}

// The CPU pipeline: poll the source at the frame rate while the screen
// changes (every 100 ms once it has been still for a second), convert on the
// CPU, encode.
static LoopEnd run_loop_cpu(Capture *c) {
    int64_t started = now_us();
    bool fallible = strcmp(c->grab->kind(), "gdi") != 0;
    int64_t last_encode = 0, last_content = 0, last_poll = 0;
    LONG applied_bitrate = c->bitrate_kbps, applied_fps = c->fps;
    bool dirty = false, sent_any = false;
    float changed = 0; // fraction of rows changed since the last encode
    int failures = 0;
    int64_t next_probe = started + kProbeFirstUs;
    int probes = 0;

    while (!c->stop) {
        LONG br = c->bitrate_kbps;
        LONG f = c->fps;
        if (br != applied_bitrate && c->enc) {
            encoder_set_bitrate(c->enc, br);
            applied_bitrate = br;
        }
#ifdef RC_HAVE_VPX
        if (c->vpx && (br != applied_bitrate || f != applied_fps)) {
            rc_vpx_rate(c->vpx, br, f);
            applied_bitrate = br;
            applied_fps = f;
        }
#endif
        int64_t interval = 1000000 / (f > 0 ? f : 30);
        int64_t now = now_us();
        int64_t poll = now - last_content < 1000000 || interval > 100000 ? interval : 100000;
        if (now - last_poll >= poll) {
            last_poll = now;
            float part = 0;
            if (c->grab->grab(&part)) {
                failures = 0;
                if (part > 0) {
                    changed = dirty ? (changed + part > 1 ? 1 : changed + part) : part;
                    dirty = true;
                    last_content = now_us();
                }
            } else if (++failures > 3) {
                if (fallible && !c->grab->have() && now_us() - started > kGpuGraceUs) {
                    logf("capture: %s source failing with no frame yet", c->grab->kind());
                    return kLoopBlind;
                }
                Sleep(50);
            }
            if (fallible && c->grab->have() && c->grab->stale(now_us()) && now_us() - started > kGpuGraceUs) {
                logf("capture: %s source stopped delivering frames", c->grab->kind());
                return kLoopStale;
            }
            if (fallible && now_us() >= next_probe) {
                next_probe = now_us() + kProbeUs;
                bool have = c->grab->have();
                float dup = have ? rc_dark_fraction(c->grab->pixels(), (int32_t)c->dst_w, (int32_t)c->dst_h,
                                                    c->grab->stride())
                                 : 1;
                if (blacked_out(c, dup, &probes)) {
                    logf("capture: %s shows %s where GDI sees the desktop", c->grab->kind(),
                         have ? "black" : "nothing");
                    return kLoopBlack;
                }
            }
        }

        now = now_us();
        bool key = InterlockedExchange(&c->keyframe_req, 0) != 0;
        if (key && c->grab->have()) dirty = true;
        LONG refine = InterlockedExchange(&c->refine_qp, 0);
        if (dirty || !sent_any || now - last_content < 100000) refine = 0;
        if ((!dirty && !refine) || !c->grab->have() || now - last_encode < interval - 1000) {
            if (key) InterlockedExchange(&c->keyframe_req, 1);
            if (refine) InterlockedCompareExchange(&c->refine_qp, refine, 0);
            int64_t wake = last_poll + poll;
            if (dirty && last_encode + interval < wake) wake = last_encode + interval;
            int64_t ms = (wake - now_us()) / 1000;
            Sleep((DWORD)(ms < 1 ? 1 : ms > 20 ? 20 : ms));
            continue;
        }

        bool force = key || !sent_any;
        bool fatal = false;
        bool accepted;
        int32_t flags = refine ? RC_FRAME_STATIC : (dirty && changed <= 0.02f) ? RC_FRAME_MINOR : 0;
#ifdef RC_HAVE_VPX
        if (c->vpx) {
            rc_vpx_from_bgra(c->vpx, c->grab->pixels(), c->grab->stride());
            accepted = rc_vpx_encode(c->vpx, now, force, refine > 0 ? (int)refine : 0, flags, c->on_frame, c->user) ==
                       RC_OK;
        } else
#endif
        {
            // Windows 7's encoder cannot be told to send an IDR: a new one starts with one.
            if (force && sent_any && !encoder_can_force_key(c->enc) && !make_encoder(c, false)) {
                logf("capture: no encoder available, stopping");
                return kLoopStopped;
            }
            uint8_t *y = c->nv12.data();
            rc_bgra_to_nv12(c->grab->pixels(), c->grab->stride(), (int)c->dst_w, (int)c->dst_h, y, (int)c->dst_w,
                            y + (size_t)c->dst_w * c->dst_h, (int)c->dst_w);
            accepted = encoder_encode_nv12(c->enc, y, now, force, &fatal, refine > 0 ? (int)refine : 0, flags);
        }
        if (accepted) {
            last_encode = now;
            dirty = false;
            changed = 0;
            sent_any = true;
        } else if (force) {
            InterlockedExchange(&c->keyframe_req, 1);
        }
        if (fatal) {
            logf("capture: software encoder failed, recreating it");
            if (!make_encoder(c, false)) {
                logf("capture: no encoder available, stopping");
                return kLoopStopped;
            }
            sent_any = false;
        }
    }
    return kLoopStopped;
}

// GDI instead of the D3D11 pipeline: before Windows 8, or when RC_CAPTURE=gdi
// (to try this path on a newer system).
static bool use_cpu_pipeline() {
    char v[8] = {0};
    DWORD n = GetEnvironmentVariableA("RC_CAPTURE", v, sizeof(v));
    return os_before_win8() || (n > 0 && n < sizeof(v) && _stricmp(v, "gdi") == 0);
}

// Starts the CPU pipeline on Desktop Duplication read back (dxgi) or GDI; an
// RC_* result.
static int start_cpu(Capture *c, bool have_mf, bool dxgi) {
    rc_capture_config cfg = c->cfg;
    if (!c->full_color && os_before_win8()) {
        // Windows 7's H.264 encoder takes at most 1920×1088.
        cfg.max_width = cfg.max_width > 0 && cfg.max_width < 1920 ? cfg.max_width : 1920;
        cfg.max_height = cfg.max_height > 0 && cfg.max_height < 1088 ? cfg.max_height : 1088;
    }
    target_size(cfg, c->mon.pub.width, c->mon.pub.height, &c->dst_w, &c->dst_h);
    if (dxgi) {
        if (!create_device(c, false)) return RC_ERR;
        c->grab = new DxgiReadback(c->dev.get(), c->ctx.get(), c->output.get(), c->dst_w, c->dst_h);
    } else {
        GdiGrabber *g = new GdiGrabber();
        c->grab = g;
        if (!g->open(c->mon.rect, c->dst_w, c->dst_h)) return RC_ERR;
    }
    if (c->full_color) {
#ifdef RC_HAVE_VPX
        c->vpx = rc_vpx_open((int)c->dst_w, (int)c->dst_h, c->cfg.fps, c->cfg.bitrate_kbps);
        if (c->vpx) {
            c->encoder = "libvpx-vp9";
            InterlockedExchange(&c->refine_ok, 1);
            return RC_OK;
        }
#endif
        logf("capture: no full-color encoder");
        return RC_ERR_UNSUPPORTED;
    }
    c->nv12.resize((size_t)c->dst_w * c->dst_h * 3 / 2);
    if (!make_encoder(c, false)) {
        logf("capture: no H.264 encoder");
        return have_mf ? RC_ERR : RC_ERR_UNSUPPORTED;
    }
    logf("capture: %s %ux%u -> %ux%u, %s", c->grab->kind(), c->mon.pub.width, c->mon.pub.height, c->dst_w, c->dst_h,
         c->encoder);
    return RC_OK;
}

// Releases the D3D11 pipeline and the encoders (a failed start, a switch to
// GDI, the end of capture).
static void release_pipeline(Capture *c) {
    delete c->src;
    c->src = nullptr;
    delete c->grab;
    c->grab = nullptr;
    if (c->enc) encoder_destroy(c->enc);
    c->enc = nullptr;
#ifdef RC_HAVE_VPX
    rc_vpx_close(c->vpx);
    c->vpx = nullptr;
#endif
    c->staging.reset();
    for (int i = 0; i < kRing; i++) {
        c->ring_view[i].reset();
        c->ring[i].reset();
    }
    c->last_view.reset();
    c->last.reset();
    c->vp.reset();
    c->venum.reset();
    c->vctx.reset();
    c->vdev.reset();
    c->ctx.reset();
    c->dev.reset();
    c->output.reset();
    c->src_w = c->src_h = 0;
    c->src_fmt = DXGI_FORMAT_UNKNOWN;
}

// Starts the D3D11 pipeline (Desktop Duplication / WGC, video processor,
// GPU encoder); an RC_* result.
static int start_gpu(Capture *c, bool have_mf) {
    if (!create_device(c, true)) return RC_ERR;
    target_size(c->cfg, c->mon.pub.width, c->mon.pub.height, &c->dst_w, &c->dst_h);
    if (!setup_processor(c, c->mon.pub.width, c->mon.pub.height, DXGI_FORMAT_B8G8R8A8_UNORM)) {
        logf("capture: video processor unavailable");
        return RC_ERR;
    }
    if (c->full_color) {
#ifdef RC_HAVE_VPX
        c->vpx = rc_vpx_open((int)c->dst_w, (int)c->dst_h, c->cfg.fps, c->cfg.bitrate_kbps);
        bool ok = c->vpx != nullptr;
#else
        bool ok = false;
#endif
        if (!ok) {
            logf("capture: no full-color encoder");
            return RC_ERR_UNSUPPORTED;
        }
        c->encoder = "libvpx-vp9";
        InterlockedExchange(&c->refine_ok, 1);
    } else if (!make_encoder(c, true)) {
        logf("capture: no H.264 encoder");
        return have_mf ? RC_ERR : RC_ERR_UNSUPPORTED;
    }
    c->src = open_source(c);
    return RC_OK;
}

// Re-reads the capture's monitor (the same id, else the primary): after a
// session moved between the console and RDP its output may be another one.
static void refresh_monitor(Capture *c) {
    enumerate_monitors(nullptr, 0);
    MonitorInfo m;
    if (find_monitor(c->mon.pub.id, &m) || find_primary_monitor(&m)) c->mon = m;
}

// Moves a capture to the CPU pipeline: Desktop Duplication read back when
// dxgi (and the monitor is a DXGI output), else or failing that GDI; an RC_*
// result.
static int switch_to_cpu(Capture *c, bool have_mf, bool dxgi) {
    int r = RC_ERR;
    for (int pass = dxgi && !c->mon.gdi ? 0 : 1; pass < 2; pass++) {
        release_pipeline(c);
        c->cpu = true;
        r = start_cpu(c, have_mf, pass == 0);
        logf("capture: CPU pipeline on %s %s (%d)", pass == 0 ? "dxgi-readback" : "gdi",
             r == RC_OK ? "started" : "failed", r);
        if (r == RC_OK || r == RC_ERR_UNSUPPORTED) break; // no encoder: GDI would not help
    }
    return r;
}

static DWORD WINAPI capture_thread(void *p) {
    Capture *c = (Capture *)p;
    CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    bool have_mf = mf_startup();
    timeBeginPeriod(1);
    attach_input_desktop();

    // GDI where D3D11 cannot work: Windows 7, a monitor DXGI does not list.
    // A D3D11 pipeline that fails to start (a server's display-only adapter
    // has no video processor or GPU encoder) reads Desktop Duplication back
    // to the CPU instead, and GDI is the last resort.
    int result;
    if (use_cpu_pipeline() || c->mon.gdi) {
        c->cpu = true;
        result = start_cpu(c, have_mf, false);
    } else {
        result = start_gpu(c, have_mf);
        if (result != RC_OK) {
            logf("capture: D3D11 pipeline failed to start (%d)", result);
            result = switch_to_cpu(c, have_mf, true);
        }
    }

    c->start_result = result;
    if (result == RC_OK && c->on_shape && c->on_pos) cursor_start(c->mon.rect, c->on_shape, c->on_pos, c->user);
    SetEvent(c->started);

    if (result == RC_OK) {
        // A failing source is rebuilt on a fresh device and output: Desktop
        // Duplication again (twice at most while it delivers nothing), then GDI;
        // a black one goes to GDI at once.
        int blind = 0;
        for (;;) {
            LoopEnd end = c->cpu ? run_loop_cpu(c) : run_loop(c);
            if (c->stop || end == kLoopStopped) break;
            if (end == kLoopStale) blind = 0;
            RECT before = c->mon.rect;
            refresh_monitor(c);
            bool dxgi = end == kLoopStale || (end == kLoopBlind && ++blind <= 2);
            if (switch_to_cpu(c, have_mf, dxgi) != RC_OK) break;
            if (memcmp(&before, &c->mon.rect, sizeof before) != 0 && c->on_shape && c->on_pos) {
                cursor_stop();
                cursor_start(c->mon.rect, c->on_shape, c->on_pos, c->user);
            }
        }
        cursor_stop();
    }
    release_pipeline(c);

    timeEndPeriod(1);
    if (have_mf) mf_shutdown();
    CoUninitialize();
    return 0;
}

int capture_start(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                  rc_cursor_pos_fn on_pos, void *user) {
    if (!cfg || !on_frame) return RC_ERR;
    Guard g(g_cap_lock);
    if (g_cap) return RC_ERR_BUSY;

    Capture *c = new Capture();
    c->cfg = *cfg;
    c->full_color = cfg->codec == RC_CODEC_VP9;
    if (c->cfg.fps <= 0 || c->cfg.fps > 60) c->cfg.fps = c->cfg.fps <= 0 ? 30 : 60;
    if (c->cfg.bitrate_kbps <= 0) c->cfg.bitrate_kbps = 4000;
    if (c->cfg.keyframe_interval_ms <= 0) c->cfg.keyframe_interval_ms = 2000;
    c->on_frame = on_frame;
    c->on_shape = on_shape;
    c->on_pos = on_pos;
    c->user = user;
    c->fps = c->cfg.fps;
    c->bitrate_kbps = c->cfg.bitrate_kbps;
    if (!find_monitor(cfg->monitor_id, &c->mon) && !find_primary_monitor(&c->mon)) {
        logf("capture: no monitor %u and no primary monitor", cfg->monitor_id);
        delete c;
        return RC_ERR;
    }
    c->started = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    c->thread = CreateThread(nullptr, 0, capture_thread, c, 0, nullptr);
    if (!c->thread) {
        CloseHandle(c->started);
        delete c;
        return RC_ERR;
    }
    WaitForSingleObject(c->started, INFINITE);
    if (c->start_result != RC_OK) {
        WaitForSingleObject(c->thread, INFINITE);
        CloseHandle(c->thread);
        CloseHandle(c->started);
        int r = c->start_result;
        delete c;
        return r;
    }
    g_cap = c;
    return RC_OK;
}

void capture_stop() {
    Capture *c;
    {
        Guard g(g_cap_lock);
        c = g_cap;
        g_cap = nullptr;
    }
    if (!c) return;
    InterlockedExchange(&c->stop, 1);
    WaitForSingleObject(c->thread, INFINITE);
    CloseHandle(c->thread);
    CloseHandle(c->started);
    delete c;
}

void capture_configure(int32_t bitrate_kbps, int32_t fps) {
    Guard g(g_cap_lock);
    if (!g_cap) return;
    if (bitrate_kbps > 0) InterlockedExchange(&g_cap->bitrate_kbps, bitrate_kbps);
    if (fps > 0) InterlockedExchange(&g_cap->fps, fps > 60 ? 60 : fps);
}

void capture_request_keyframe() {
    Guard g(g_cap_lock);
    if (g_cap) InterlockedExchange(&g_cap->keyframe_req, 1);
}

int capture_refine(int32_t max_qp) {
    Guard g(g_cap_lock);
    if (!g_cap) return RC_ERR;
    if (max_qp > 0 && !g_cap->refine_ok) return RC_ERR_UNSUPPORTED;
    InterlockedExchange(&g_cap->refine_qp, max_qp <= 0 ? -1 : max_qp > 51 ? 51 : max_qp);
    return RC_OK;
}

const char *capture_encoder_name() {
    Guard g(g_cap_lock);
    return g_cap ? g_cap->encoder : nullptr;
}

} // namespace rcw

namespace rcw {

// ---------------------------------------------------------------- preview

// One GDI copy of the monitor, scaled down with HALFTONE: available on every
// Windows version (the Windows 7 build too) and cheap once a minute. Holds
// g_cap_lock, so a capture cannot start meanwhile.
int capture_snapshot(uint32_t monitor_id, int32_t max_w, int32_t max_h, rc_image *out) {
    memset(out, 0, sizeof *out);
    Guard g(g_cap_lock);
    if (g_cap) return RC_ERR_BUSY;
    if (!input_desktop_is_default()) return RC_ERR_LOCKED; // lock screen, UAC: never copied
    MonitorInfo mon = {};
    bool found = monitor_id != 0 && find_monitor(monitor_id, &mon);
    if (!found) {
        MonitorInfo all[16];
        int n = enumerate_monitors(all, 16);
        for (int i = 0; i < n && i < 16 && !found; i++) {
            if (all[i].pub.primary || (monitor_id == 0 && i == n - 1)) {
                mon = all[i];
                found = true;
            }
        }
    }
    if (!found) return RC_ERR;
    int sw = mon.rect.right - mon.rect.left, sh = mon.rect.bottom - mon.rect.top;
    int32_t w, h;
    rc_fit(sw, sh, max_w, max_h, &w, &h);

    HDC screen = GetDC(nullptr);
    if (!screen) return RC_ERR;
    HDC mem = CreateCompatibleDC(screen);
    BITMAPINFO bi = {};
    bi.bmiHeader.biSize = sizeof(bi.bmiHeader);
    bi.bmiHeader.biWidth = w;
    bi.bmiHeader.biHeight = -h; // top-down
    bi.bmiHeader.biPlanes = 1;
    bi.bmiHeader.biBitCount = 32;
    bi.bmiHeader.biCompression = BI_RGB;
    void *bits = nullptr;
    HBITMAP bmp = mem ? CreateDIBSection(screen, &bi, DIB_RGB_COLORS, &bits, nullptr, 0) : nullptr;
    int rc = RC_ERR;
    if (bmp) {
        HGDIOBJ old = SelectObject(mem, bmp);
        SetStretchBltMode(mem, HALFTONE);
        SetBrushOrgEx(mem, 0, 0, nullptr);
        BOOL ok = StretchBlt(mem, 0, 0, w, h, screen, mon.rect.left, mon.rect.top, sw, sh, SRCCOPY);
        SelectObject(mem, old);
        GdiFlush();
        if (ok) {
            size_t n = (size_t)w * h * 4;
            out->pixels = (uint8_t *)malloc(n);
            if (out->pixels) {
                memcpy(out->pixels, bits, n);
                for (size_t i = 3; i < n; i += 4) out->pixels[i] = 255;
                out->width = w;
                out->height = h;
                out->stride = w * 4;
                rc = RC_OK;
            }
        }
        DeleteObject(bmp);
    }
    if (mem) DeleteDC(mem);
    ReleaseDC(nullptr, screen);
    return rc;
}

} // namespace rcw
