// Capture thread: grabs desktop frames (Windows.Graphics.Capture on the user
// desktop when built with RC_HAVE_WGC, DXGI Desktop Duplication otherwise and
// always on secure desktops), converts BGRA → NV12 with the D3D11 video
// processor, and feeds the MF encoder. Full color (RC_CODEC_VP9) scales to
// BGRA instead, reads it back and encodes with rc_vpx on this thread. One
// capture at a time.
#include "rc_win.h"

#ifdef RC_HAVE_VPX
#include "rc_vpx.h"
#endif

#include <d3d10.h>
#include <dxgi1_5.h>
#include <mfapi.h>
#include <mmsystem.h>

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
    volatile LONG refine_qp = 0;  // pending rc_encode_refine, 0 = none
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

static bool create_device(Capture *c) {
    Com<IDXGIFactory1> factory;
    if (FAILED(CreateDXGIFactory1(__uuidof(IDXGIFactory1), factory.put_void()))) return false;
    Com<IDXGIAdapter1> adapter;
    if (FAILED(factory->EnumAdapters1(c->mon.adapter_index, adapter.put()))) return false;
    Com<IDXGIOutput> out;
    if (FAILED(adapter->EnumOutputs(c->mon.output_index, out.put()))) return false;
    if (FAILED(out.as(c->output))) return false;

    D3D_FEATURE_LEVEL levels[] = {D3D_FEATURE_LEVEL_11_1, D3D_FEATURE_LEVEL_11_0};
    UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT;
    HRESULT hr = D3D11CreateDevice(adapter.get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, flags, levels, 2,
                                   D3D11_SDK_VERSION, c->dev.put(), nullptr, c->ctx.put());
    if (hr == E_INVALIDARG)
        hr = D3D11CreateDevice(adapter.get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, flags, levels + 1, 1,
                               D3D11_SDK_VERSION, c->dev.put(), nullptr, c->ctx.put());
    if (FAILED(hr)) {
        logf("capture: D3D11CreateDevice failed 0x%08lx", (unsigned long)hr);
        return false;
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

static void run_loop(Capture *c) {
    int64_t interval = 1000000 / (c->fps > 0 ? c->fps : 30);
    int64_t last_encode = 0, last_content = 0;
    int64_t last_desktop_check = now_us();
    LONG applied_bitrate = c->bitrate_kbps, applied_fps = c->fps;
    bool dirty = false, have = false, sent_any = false;
    float changed = 0; // fraction of the frame changed since the last encode, -1 unknown
    int idx = 0;
    int failures = 0;

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
            accepted = encode_full_color(c, idx, now, key || !sent_any, (int)refine, flags);
        else
#endif
            accepted = encoder_encode(c->enc, c->ring[idx].get(), now, key || !sent_any, &fatal, (int)refine, flags);
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
            encoder_destroy(c->enc);
            EncoderConfig ec = {c->dst_w, c->dst_h, (int)c->fps, (int)c->bitrate_kbps, c->cfg.keyframe_interval_ms,
                              c->cfg.profile_idc};
            c->enc = encoder_create(c->dev.get(), ec, false, c->on_frame, c->user);
            if (!c->enc) {
                logf("capture: no encoder available, stopping");
                return;
            }
            c->encoder = encoder_name(c->enc);
            InterlockedExchange(&c->refine_ok, encoder_can_refine(c->enc) ? 1 : 0);
            sent_any = false;
        }
    }
}

static DWORD WINAPI capture_thread(void *p) {
    Capture *c = (Capture *)p;
    CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    bool have_mf = mf_startup();
    timeBeginPeriod(1);
    attach_input_desktop();

    int result = RC_ERR;
    do {
        if (!create_device(c)) break;
        target_size(c->cfg, c->mon.pub.width, c->mon.pub.height, &c->dst_w, &c->dst_h);
        if (!setup_processor(c, c->mon.pub.width, c->mon.pub.height, DXGI_FORMAT_B8G8R8A8_UNORM)) {
            logf("capture: video processor unavailable");
            break;
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
                result = RC_ERR_UNSUPPORTED;
                break;
            }
            c->encoder = "libvpx-vp9";
            InterlockedExchange(&c->refine_ok, 1);
        } else {
            EncoderConfig ec = {c->dst_w, c->dst_h, c->cfg.fps, c->cfg.bitrate_kbps, c->cfg.keyframe_interval_ms,
                                c->cfg.profile_idc};
            c->enc = encoder_create(c->dev.get(), ec, true, c->on_frame, c->user);
            if (!c->enc) {
                logf("capture: no H.264 encoder");
                result = have_mf ? RC_ERR : RC_ERR_UNSUPPORTED;
                break;
            }
            c->encoder = encoder_name(c->enc);
            InterlockedExchange(&c->refine_ok, encoder_can_refine(c->enc) ? 1 : 0);
        }
        c->src = open_source(c);
        result = RC_OK;
    } while (0);

    c->start_result = result;
    if (result == RC_OK && c->on_shape && c->on_pos) cursor_start(c->mon.rect, c->on_shape, c->on_pos, c->user);
    SetEvent(c->started);

    if (result == RC_OK) {
        run_loop(c);
        cursor_stop();
    }

    delete c->src;
    c->src = nullptr;
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
    if (!find_monitor(cfg->monitor_id, &c->mon)) {
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
    if (!g_cap->refine_ok) return RC_ERR_UNSUPPORTED;
    InterlockedExchange(&g_cap->refine_qp, max_qp < 1 ? 1 : max_qp > 51 ? 51 : max_qp);
    return RC_OK;
}

const char *capture_encoder_name() {
    Guard g(g_cap_lock);
    return g_cap ? g_cap->encoder : nullptr;
}

} // namespace rcw
