// Media Foundation H.264 encoder: hardware async MFT on the shared D3D11
// device when available ("mf-hw"), the synchronous software MFT otherwise
// ("mf-sw"). Input is NV12; output is Annex-B access units with SPS/PPS
// in front of every IDR.
#include "rc_win.h"

#include <mfapi.h>
#include <mfidl.h>
#include <mferror.h>
#include <mftransform.h>
#include <codecapi.h>
#include <strmif.h>
#include <stdlib.h>

#include <vector>

namespace rcw {

// mingw-w64 declares neither IID_ICodecAPI nor a __uuidof for it.
static const GUID kIID_ICodecAPI = {0x901db4c7, 0x31ce, 0x41a2, {0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda}};

static void set_size(IMFAttributes *a, const GUID &k, UINT32 hi, UINT32 lo) {
    a->SetUINT64(k, ((UINT64)hi << 32) | lo);
}

static void codec_ui4(ICodecAPI *c, const GUID &g, ULONG v) {
    if (!c) return;
    VARIANT var;
    VariantInit(&var);
    var.vt = VT_UI4;
    var.ulVal = v;
    HRESULT hr = c->SetValue(&g, &var);
    if (FAILED(hr)) logf("encoder: ICodecAPI SetValue (ui4) failed 0x%08lx", (unsigned long)hr);
}

static void codec_bool(ICodecAPI *c, const GUID &g, bool v) {
    if (!c) return;
    VARIANT var;
    VariantInit(&var);
    var.vt = VT_BOOL;
    var.boolVal = v ? VARIANT_TRUE : VARIANT_FALSE;
    c->SetValue(&g, &var);
}

// Returns the NAL unit types present in an Annex-B buffer as a bitmask.
static uint32_t nal_types(const uint8_t *p, size_t n) {
    uint32_t mask = 0;
    for (size_t i = 0; i + 3 < n; i++) {
        if (p[i] == 0 && p[i + 1] == 0 && p[i + 2] == 1) {
            mask |= 1u << (p[i + 3] & 0x1f);
            i += 3;
        }
    }
    return mask;
}

class Encoder {
public:
    EncoderConfig cfg{};
    rc_frame_fn on_frame = nullptr;
    void *user = nullptr;

    ID3D11Device *dev = nullptr; // borrowed
    Com<ID3D11DeviceContext> ctx;
    Com<IMFTransform> mft;
    Com<ICodecAPI> codec;
    Com<IMFDXGIDeviceManager> dm;
    Com<IMFMediaEventGenerator> events;
    Com<ID3D11Texture2D> staging; // software path readback

    bool async = false;
    bool d3d_aware = false;
    bool hw = false;
    bool provides_samples = false;
    DWORD out_cb = 0;
    DWORD in_id = 0, out_id = 0;

    volatile LONG need_input = 0;
    volatile LONG stopping = 0;
    HANDLE event_thread = nullptr;

    Lock out_lock;
    std::vector<uint8_t> seq_header; // Annex-B SPS+PPS from the output type
    std::vector<uint8_t> scratch;
    int64_t base_pts = -1;
    const char *name = "mf-sw";

    ~Encoder() { shutdown(); }

    bool configure() {
        Com<IMFAttributes> attrs;
        if (SUCCEEDED(mft->GetAttributes(attrs.put())) && attrs) {
            UINT32 v = 0;
            if (SUCCEEDED(attrs->GetUINT32(MF_TRANSFORM_ASYNC, &v)) && v) {
                async = true;
                attrs->SetUINT32(MF_TRANSFORM_ASYNC_UNLOCK, TRUE);
            }
            v = 0;
            if (SUCCEEDED(attrs->GetUINT32(MF_SA_D3D11_AWARE, &v)) && v) d3d_aware = true;
            attrs->SetUINT32(MF_LOW_LATENCY, TRUE);
        }
        if (d3d_aware && dm) {
            if (FAILED(mft->ProcessMessage(MFT_MESSAGE_SET_D3D_MANAGER, (ULONG_PTR)dm.get()))) d3d_aware = false;
        }

        HRESULT hr = mft->GetStreamIDs(1, &in_id, 1, &out_id);
        if (hr == E_NOTIMPL) { in_id = 0; out_id = 0; }

        mft->QueryInterface(kIID_ICodecAPI, codec.put_void());
        apply_codec_settings();

        // Output type first (the encoder needs it to accept an input type).
        if (!set_output_type(eAVEncH264VProfile_Main) && !set_output_type(eAVEncH264VProfile_Base)) {
            logf("encoder: no acceptable H.264 output type");
            return false;
        }
        if (!set_input_type()) {
            logf("encoder: NV12 input type rejected");
            return false;
        }
        apply_codec_settings();

        MFT_OUTPUT_STREAM_INFO si = {};
        if (SUCCEEDED(mft->GetOutputStreamInfo(out_id, &si))) {
            provides_samples = (si.dwFlags & (MFT_OUTPUT_STREAM_PROVIDES_SAMPLES |
                                              MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES)) != 0;
            out_cb = si.cbSize;
        }
        if (out_cb < cfg.width * cfg.height) out_cb = cfg.width * cfg.height; // generous for IDRs

        if (!d3d_aware) {
            D3D11_TEXTURE2D_DESC td = {};
            td.Width = cfg.width;
            td.Height = cfg.height;
            td.MipLevels = 1;
            td.ArraySize = 1;
            td.Format = DXGI_FORMAT_NV12;
            td.SampleDesc.Count = 1;
            td.Usage = D3D11_USAGE_STAGING;
            td.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
            if (FAILED(dev->CreateTexture2D(&td, nullptr, staging.put()))) {
                logf("encoder: cannot create NV12 staging texture");
                return false;
            }
        }

        if (async) {
            if (FAILED(mft.as(events))) return false;
        }
        mft->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
        mft->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
        if (async) {
            event_thread = CreateThread(nullptr, 0, &Encoder::event_loop, this, 0, nullptr);
            if (!event_thread) return false;
        }
        return true;
    }

    void apply_codec_settings() {
        if (!codec) return;
        codec_bool(codec.get(), CODECAPI_AVLowLatencyMode, true);
        codec_ui4(codec.get(), CODECAPI_AVEncCommonRateControlMode, eAVEncCommonRateControlMode_CBR);
        codec_ui4(codec.get(), CODECAPI_AVEncCommonMeanBitRate, (ULONG)cfg.bitrate_kbps * 1000);
        ULONG gop = (ULONG)((int64_t)cfg.fps * (cfg.keyframe_interval_ms > 0 ? cfg.keyframe_interval_ms : 2000) / 1000);
        codec_ui4(codec.get(), CODECAPI_AVEncMPVGOPSize, gop ? gop : 1);
        codec_ui4(codec.get(), CODECAPI_AVEncMPVDefaultBPictureCount, 0);
        codec_ui4(codec.get(), CODECAPI_AVEncCommonQualityVsSpeed, 30);
    }

    bool set_output_type(UINT32 profile) {
        Com<IMFMediaType> t;
        if (FAILED(mf().CreateMediaType(t.put()))) return false;
        t->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        t->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
        t->SetUINT32(MF_MT_AVG_BITRATE, (UINT32)cfg.bitrate_kbps * 1000);
        set_size(t.get(), MF_MT_FRAME_SIZE, cfg.width, cfg.height);
        set_size(t.get(), MF_MT_FRAME_RATE, (UINT32)cfg.fps, 1);
        set_size(t.get(), MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
        t->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
        t->SetUINT32(MF_MT_MPEG2_PROFILE, profile);
        t->SetUINT32(MF_MT_YUV_MATRIX, MFVideoTransferMatrix_BT709);
        t->SetUINT32(MF_MT_VIDEO_NOMINAL_RANGE, MFNominalRange_16_235);
        HRESULT hr = mft->SetOutputType(out_id, t.get(), 0);
        if (FAILED(hr)) return false;
        refresh_seq_header();
        return true;
    }

    bool set_input_type() {
        // Prefer the encoder's own NV12 type so vendor attributes survive.
        for (DWORD i = 0;; i++) {
            Com<IMFMediaType> t;
            HRESULT hr = mft->GetInputAvailableType(in_id, i, t.put());
            if (hr == MF_E_NO_MORE_TYPES || FAILED(hr)) break;
            GUID sub = {};
            t->GetGUID(MF_MT_SUBTYPE, &sub);
            if (sub != MFVideoFormat_NV12) continue;
            set_size(t.get(), MF_MT_FRAME_SIZE, cfg.width, cfg.height);
            set_size(t.get(), MF_MT_FRAME_RATE, (UINT32)cfg.fps, 1);
            if (SUCCEEDED(mft->SetInputType(in_id, t.get(), 0))) return true;
        }
        Com<IMFMediaType> t;
        if (FAILED(mf().CreateMediaType(t.put()))) return false;
        t->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        t->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_NV12);
        set_size(t.get(), MF_MT_FRAME_SIZE, cfg.width, cfg.height);
        set_size(t.get(), MF_MT_FRAME_RATE, (UINT32)cfg.fps, 1);
        set_size(t.get(), MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
        t->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
        return SUCCEEDED(mft->SetInputType(in_id, t.get(), 0));
    }

    void refresh_seq_header() {
        Com<IMFMediaType> cur;
        if (FAILED(mft->GetOutputCurrentType(out_id, cur.put()))) return;
        UINT32 sz = 0;
        if (FAILED(cur->GetBlobSize(MF_MT_MPEG_SEQUENCE_HEADER, &sz)) || sz == 0) return;
        std::vector<uint8_t> b(sz);
        if (FAILED(cur->GetBlob(MF_MT_MPEG_SEQUENCE_HEADER, b.data(), sz, &sz))) return;
        b.resize(sz);
        // Only usable when already Annex-B.
        if (sz >= 4 && b[0] == 0 && b[1] == 0 && (b[2] == 1 || (b[2] == 0 && b[3] == 1))) seq_header.swap(b);
    }

    static DWORD WINAPI event_loop(void *p) {
        Encoder *e = (Encoder *)p;
        CoInitializeEx(nullptr, COINIT_MULTITHREADED);
        for (;;) {
            Com<IMFMediaEvent> ev;
            if (FAILED(e->events->GetEvent(0, ev.put()))) break;
            MediaEventType t = MEUnknown;
            ev->GetType(&t);
            if (t == METransformNeedInput) {
                InterlockedIncrement(&e->need_input);
            } else if (t == METransformHaveOutput) {
                bool more = false;
                e->pull_output(&more);
            } else if (t == METransformDrainComplete) {
                break;
            }
        }
        CoUninitialize();
        return 0;
    }

    // Pulls one output sample. *need_more = encoder wants more input.
    bool pull_output(bool *need_more) {
        *need_more = false;
        MFT_OUTPUT_DATA_BUFFER ob = {};
        ob.dwStreamID = out_id;
        Com<IMFSample> own;
        if (!provides_samples) {
            Com<IMFMediaBuffer> buf;
            if (FAILED(mf().CreateSample(own.put())) || FAILED(mf().CreateMemoryBuffer(out_cb, buf.put()))) return false;
            own->AddBuffer(buf.get());
            ob.pSample = own.get();
        }
        DWORD status = 0;
        HRESULT hr = mft->ProcessOutput(0, 1, &ob, &status);
        if (ob.pEvents) ob.pEvents->Release();
        if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
            *need_more = true;
            return true;
        }
        if (hr == MF_E_TRANSFORM_STREAM_CHANGE) {
            Com<IMFMediaType> t;
            if (SUCCEEDED(mft->GetOutputAvailableType(out_id, 0, t.put()))) mft->SetOutputType(out_id, t.get(), 0);
            refresh_seq_header();
            return true;
        }
        if (FAILED(hr)) {
            logf("encoder: ProcessOutput failed 0x%08lx", (unsigned long)hr);
            return false;
        }
        IMFSample *s = ob.pSample;
        if (s) deliver(s);
        if (provides_samples && s) s->Release();
        return true;
    }

    void deliver(IMFSample *s) {
        Com<IMFMediaBuffer> buf;
        if (FAILED(s->ConvertToContiguousBuffer(buf.put()))) return;
        BYTE *data = nullptr;
        DWORD len = 0;
        if (FAILED(buf->Lock(&data, nullptr, &len))) return;
        UINT32 clean = 0;
        s->GetUINT32(MFSampleExtension_CleanPoint, &clean);
        uint32_t types = nal_types(data, len);
        bool key = clean != 0 || (types & (1u << 5)) != 0;
        LONGLONG t = 0;
        s->GetSampleTime(&t);
        int64_t pts = (base_pts < 0 ? 0 : base_pts) + t / 10;

        Guard g(out_lock);
        if (!stopping && on_frame) {
            if (key && !(types & (1u << 7)) && !seq_header.empty()) {
                scratch.resize(seq_header.size() + len);
                memcpy(scratch.data(), seq_header.data(), seq_header.size());
                memcpy(scratch.data() + seq_header.size(), data, len);
                on_frame(user, scratch.data(), (int32_t)scratch.size(), pts, 1);
            } else {
                on_frame(user, data, (int32_t)len, pts, key ? 1 : 0);
            }
        }
        buf->Unlock();
    }

    bool make_input(ID3D11Texture2D *nv12, IMFMediaBuffer **out) {
        if (d3d_aware) {
            Com<IMFMediaBuffer> b;
            if (FAILED(mf().CreateDXGISurfaceBuffer(__uuidof(ID3D11Texture2D), nv12, 0, FALSE, b.put()))) return false;
            Com<IMF2DBuffer> b2;
            DWORD clen = 0;
            if (SUCCEEDED(b.as(b2)) && SUCCEEDED(b2->GetContiguousLength(&clen))) b->SetCurrentLength(clen);
            *out = b.get();
            (*out)->AddRef();
            return true;
        }
        ctx->CopyResource(staging.get(), nv12);
        D3D11_MAPPED_SUBRESOURCE m;
        if (FAILED(ctx->Map(staging.get(), 0, D3D11_MAP_READ, 0, &m))) return false;
        DWORD size = cfg.width * cfg.height * 3 / 2;
        Com<IMFMediaBuffer> b;
        BYTE *dst = nullptr;
        if (FAILED(mf().CreateMemoryBuffer(size, b.put())) || FAILED(b->Lock(&dst, nullptr, nullptr))) {
            ctx->Unmap(staging.get(), 0);
            return false;
        }
        const BYTE *src = (const BYTE *)m.pData;
        for (UINT y = 0; y < cfg.height; y++) memcpy(dst + y * cfg.width, src + y * m.RowPitch, cfg.width);
        const BYTE *uv = src + (size_t)m.RowPitch * cfg.height;
        BYTE *duv = dst + cfg.width * cfg.height;
        for (UINT y = 0; y < cfg.height / 2; y++) memcpy(duv + y * cfg.width, uv + y * m.RowPitch, cfg.width);
        b->Unlock();
        b->SetCurrentLength(size);
        ctx->Unmap(staging.get(), 0);
        *out = b.get();
        (*out)->AddRef();
        return true;
    }

    bool encode(ID3D11Texture2D *nv12, int64_t pts_us, bool force_key, bool *fatal) {
        *fatal = false;
        if (async && need_input <= 0) return false;
        if (base_pts < 0) base_pts = pts_us;

        Com<IMFSample> sample;
        Com<IMFMediaBuffer> buf;
        if (FAILED(mf().CreateSample(sample.put())) || !make_input(nv12, buf.put())) {
            *fatal = true;
            return false;
        }
        sample->AddBuffer(buf.get());
        sample->SetSampleTime((pts_us - base_pts) * 10);
        sample->SetSampleDuration(10000000LL / (cfg.fps > 0 ? cfg.fps : 30));
        if (force_key) codec_ui4(codec.get(), CODECAPI_AVEncVideoForceKeyFrame, 1);

        HRESULT hr = mft->ProcessInput(in_id, sample.get(), 0);
        if (async) InterlockedDecrement(&need_input);
        if (hr == MF_E_NOTACCEPTING) {
            if (!async) drain_sync();
            return false;
        }
        if (FAILED(hr)) {
            logf("encoder: ProcessInput failed 0x%08lx", (unsigned long)hr);
            *fatal = true;
            return false;
        }
        if (!async) drain_sync();
        return true;
    }

    void drain_sync() {
        for (int i = 0; i < 8; i++) {
            bool more = false;
            if (!pull_output(&more) || more) return;
        }
    }

    void set_bitrate(int kbps) {
        cfg.bitrate_kbps = kbps;
        codec_ui4(codec.get(), CODECAPI_AVEncCommonMeanBitRate, (ULONG)kbps * 1000);
    }

    void shutdown() {
        InterlockedExchange(&stopping, 1);
        if (mft) {
            mft->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
            if (async) mft->ProcessMessage(MFT_MESSAGE_COMMAND_DRAIN, 0);
        }
        if (event_thread) {
            if (WaitForSingleObject(event_thread, 2000) == WAIT_TIMEOUT) {
                Com<IMFShutdown> sd;
                if (SUCCEEDED(mft.as(sd))) sd->Shutdown();
                WaitForSingleObject(event_thread, 1000);
            }
            CloseHandle(event_thread);
            event_thread = nullptr;
        }
        if (mft) mft->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
        // Hardware MFTs must be shut down explicitly to release the device.
        if (mft && hw) {
            Com<IMFShutdown> sd;
            if (SUCCEEDED(mft.as(sd))) sd->Shutdown();
        }
        events.reset();
        codec.reset();
        mft.reset();
        staging.reset();
        dm.reset();
    }
};

static bool try_activate(Encoder *e, IMFActivate *act) {
    if (FAILED(act->ActivateObject(__uuidof(IMFTransform), e->mft.put_void()))) return false;
    wchar_t fname[128] = {0};
    UINT32 n = 0;
    act->GetString(MFT_FRIENDLY_NAME_Attribute, fname, 128, &n);
    char name8[256];
    wide_to_utf8(fname, name8, sizeof(name8));
    if (e->configure()) {
        logf("encoder: using %s (%s, async=%d, d3d=%d)", name8, e->hw ? "hw" : "sw", e->async, e->d3d_aware);
        return true;
    }
    logf("encoder: %s rejected the configuration", name8);
    e->shutdown();
    InterlockedExchange(&e->stopping, 0);
    e->async = e->d3d_aware = e->provides_samples = false;
    act->ShutdownObject();
    return false;
}

static bool open_from_enum(Encoder *e, UINT32 flags) {
    MFT_REGISTER_TYPE_INFO in = {MFMediaType_Video, MFVideoFormat_NV12};
    MFT_REGISTER_TYPE_INFO out = {MFMediaType_Video, MFVideoFormat_H264};
    IMFActivate **acts = nullptr;
    UINT32 count = 0;
    bool hw = (flags & MFT_ENUM_FLAG_HARDWARE) != 0;
    // Hardware MFTs often do not register their input types; filter on output only.
    if (FAILED(mf().TEnumEx(MFT_CATEGORY_VIDEO_ENCODER, flags, hw ? nullptr : &in, &out, &acts, &count))) return false;
    bool ok = false;
    for (UINT32 i = 0; i < count; i++) {
        if (!ok) {
            e->hw = hw;
            ok = try_activate(e, acts[i]);
        }
        acts[i]->Release();
    }
    CoTaskMemFree(acts);
    return ok;
}

Encoder *encoder_create(ID3D11Device *dev, const EncoderConfig &cfg, bool prefer_hw, rc_frame_fn on_frame,
                        void *user) {
    if (!mf().ok) return nullptr;
    Encoder *e = new Encoder();
    e->cfg = cfg;
    e->on_frame = on_frame;
    e->user = user;
    e->dev = dev;
    dev->GetImmediateContext(e->ctx.put());
    UINT token = 0;
    if (SUCCEEDED(mf().CreateDXGIDeviceManager(&token, e->dm.put()))) {
        if (FAILED(e->dm->ResetDevice(dev, token))) e->dm.reset();
    }
    if (prefer_hw && open_from_enum(e, MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER)) {
        e->name = "mf-hw";
        return e;
    }
    if (open_from_enum(e, MFT_ENUM_FLAG_SYNCMFT | MFT_ENUM_FLAG_SORTANDFILTER)) {
        e->name = "mf-sw";
        return e;
    }
    delete e;
    return nullptr;
}

void encoder_destroy(Encoder *e) { delete e; }

bool encoder_encode(Encoder *e, ID3D11Texture2D *nv12, int64_t pts_us, bool force_key, bool *fatal) {
    return e->encode(nv12, pts_us, force_key, fatal);
}

void encoder_set_bitrate(Encoder *e, int kbps) { e->set_bitrate(kbps); }

const char *encoder_name(Encoder *e) { return e ? e->name : "mf"; }

static const char *volatile g_probe_result = "mf-sw";

static DWORD WINAPI probe_thread(void *) {
    const char *volatile *out = &g_probe_result;
    CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    if (!mf().ok) *out = "none";
    if (mf_startup()) {
        MFT_REGISTER_TYPE_INFO outt = {MFMediaType_Video, MFVideoFormat_H264};
        IMFActivate **acts = nullptr;
        UINT32 count = 0;
        if (SUCCEEDED(mf().TEnumEx(MFT_CATEGORY_VIDEO_ENCODER, MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER,
                                nullptr, &outt, &acts, &count))) {
            if (count > 0) *out = "mf-hw";
            for (UINT32 i = 0; i < count; i++) acts[i]->Release();
            CoTaskMemFree(acts);
        }
        mf_shutdown();
    }
    CoUninitialize();
    return 0;
}

const char *encoder_probe() {
    HANDLE t = CreateThread(nullptr, 0, probe_thread, nullptr, 0, nullptr);
    if (t) {
        WaitForSingleObject(t, 5000);
        CloseHandle(t);
    }
    return g_probe_result;
}

} // namespace rcw
