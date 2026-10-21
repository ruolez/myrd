/*
 * rc_vpx.c — VP9 profile 1 (4:4:4) real-time encoder, see rc_vpx.h.
 * Settings follow libwebrtc's VP9 screenshare: one-pass CBR, no lag,
 * keyframes only on request, screen content tuning, row multithreading.
 */
#include "rc_vpx.h"

#include <stdlib.h>
#include <string.h>

#include <vpx/vp8cx.h>
#include <vpx/vpx_encoder.h>

#ifdef _WIN32
#include <windows.h>
#else
#include <unistd.h>
#endif

/* No finer than refinement's finest pass (H.264 QP 18): with spare bitrate
 * the rate control would otherwise keep re-encoding a static screen. */
#define RC_VPX_MIN_Q 22
#define RC_VPX_MAX_Q 56

struct rc_vpx {
    vpx_codec_ctx_t codec;
    vpx_codec_enc_cfg_t cfg;
    vpx_image_t *img;
    int fps;
    int clamped; /* cfg's quantizer range is a refinement pass's */
};

static int cpu_count(void) {
#ifdef _WIN32
    SYSTEM_INFO si;
    GetSystemInfo(&si);
    return (int)si.dwNumberOfProcessors;
#else
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    return n > 0 ? (int)n : 1;
#endif
}

static int clampi(int v, int lo, int hi) { return v < lo ? lo : v > hi ? hi : v; }

/* Tiles are at least 256 pixels wide; each one is a thread's column. */
static int tile_columns_log2(int width, int threads) {
    int log2 = 0;
    while (log2 < 6 && (width >> (log2 + 1)) >= 256 && (1 << (log2 + 1)) <= threads) log2++;
    return log2;
}

rc_vpx *rc_vpx_open(int width, int height, int fps, int kbps) {
    if (width < 2 || height < 2) return NULL;
    rc_vpx *e = calloc(1, sizeof *e);
    if (!e) return NULL;
    vpx_codec_enc_cfg_t *c = &e->cfg;
    if (vpx_codec_enc_config_default(vpx_codec_vp9_cx(), c, 0) != VPX_CODEC_OK) goto fail;
    int threads = clampi(cpu_count() - 1, 1, 8);
    e->fps = clampi(fps, 1, 60);
    c->g_w = (unsigned)width;
    c->g_h = (unsigned)height;
    c->g_profile = 1; /* 4:4:4 */
    c->g_bit_depth = VPX_BITS_8;
    c->g_input_bit_depth = 8;
    c->g_timebase.num = 1;
    c->g_timebase.den = 1000000;
    c->g_threads = (unsigned)threads;
    c->g_lag_in_frames = 0;
    c->g_error_resilient = 0;
    c->g_pass = VPX_RC_ONE_PASS;
    c->rc_end_usage = VPX_CBR;
    c->rc_target_bitrate = (unsigned)clampi(kbps, 100, 100000);
    c->rc_min_quantizer = RC_VPX_MIN_Q;
    c->rc_max_quantizer = RC_VPX_MAX_Q;
    c->rc_dropframe_thresh = 0;
    c->rc_resize_allowed = 0;
    c->rc_undershoot_pct = 50;
    c->rc_overshoot_pct = 15;
    c->rc_buf_initial_sz = 300;
    c->rc_buf_optimal_sz = 400;
    c->rc_buf_sz = 600;
    c->kf_mode = VPX_KF_DISABLED;
    c->kf_max_dist = 0x7fffffff;
    if (vpx_codec_enc_init(&e->codec, vpx_codec_vp9_cx(), c, 0) != VPX_CODEC_OK) goto fail;
    /* Real-time speeds are 5..9; 4:4:4 above 1080p needs the fastest. */
    vpx_codec_control(&e->codec, VP8E_SET_CPUUSED, width * height > 1920 * 1080 ? 9 : 8);
    vpx_codec_control(&e->codec, VP9E_SET_TUNE_CONTENT, VP9E_CONTENT_SCREEN);
    vpx_codec_control(&e->codec, VP9E_SET_ROW_MT, 1);
    vpx_codec_control(&e->codec, VP9E_SET_TILE_COLUMNS, tile_columns_log2(width, threads));
    vpx_codec_control(&e->codec, VP9E_SET_AQ_MODE, 3); /* cyclic refresh */
    vpx_codec_control(&e->codec, VP9E_SET_NOISE_SENSITIVITY, 0);
    vpx_codec_control(&e->codec, VP8E_SET_MAX_INTRA_BITRATE_PCT, 600);
    vpx_codec_control(&e->codec, VP9E_SET_COLOR_SPACE, VPX_CS_BT_709);
    vpx_codec_control(&e->codec, VP9E_SET_COLOR_RANGE, VPX_CR_STUDIO_RANGE);
    e->img = vpx_img_alloc(NULL, VPX_IMG_FMT_I444, (unsigned)width, (unsigned)height, 32);
    if (!e->img) {
        vpx_codec_destroy(&e->codec);
        goto fail;
    }
    e->img->cs = VPX_CS_BT_709;
    e->img->range = VPX_CR_STUDIO_RANGE;
    return e;
fail:
    free(e);
    return NULL;
}

void rc_vpx_close(rc_vpx *e) {
    if (!e) return;
    vpx_codec_destroy(&e->codec);
    vpx_img_free(e->img);
    free(e);
}

void rc_vpx_rate(rc_vpx *e, int kbps, int fps) {
    if (!e) return;
    if (kbps > 0) e->cfg.rc_target_bitrate = (unsigned)clampi(kbps, 100, 100000);
    if (fps > 0) e->fps = clampi(fps, 1, 60);
    if (!e->clamped) vpx_codec_enc_config_set(&e->codec, &e->cfg);
}

void rc_vpx_planes(rc_vpx *e, uint8_t *planes[3], int strides[3]) {
    for (int i = 0; i < 3; i++) {
        planes[i] = e->img->planes[i];
        strides[i] = e->img->stride[i];
    }
}

/* BT.709 limited range, 16-bit fixed point: Y = 16 + 219/255·(Kr R + Kg G + Kb B),
 * Cb/Cr = 128 + 224/255·(B−Y)/(2(1−Kb)), (R−Y)/(2(1−Kr)). */
void rc_bgra_to_i444(const uint8_t *bgra, int stride, int width, int height, uint8_t *y, int ys, uint8_t *u, int us,
                     uint8_t *v, int vs) {
    for (int row = 0; row < height; row++) {
        const uint8_t *p = bgra + (size_t)row * stride;
        uint8_t *yr = y + (size_t)row * ys, *ur = u + (size_t)row * us, *vr = v + (size_t)row * vs;
        for (int x = 0; x < width; x++, p += 4) {
            int b = p[0], g = p[1], r = p[2];
            yr[x] = (uint8_t)((11966 * r + 40254 * g + 4064 * b + (16 << 16) + 32768) >> 16);
            ur[x] = (uint8_t)((-6596 * r - 22189 * g + 28785 * b + (128 << 16) + 32768) >> 16);
            vr[x] = (uint8_t)((28785 * r - 26145 * g - 2640 * b + (128 << 16) + 32768) >> 16);
        }
    }
}

void rc_vpx_from_bgra(rc_vpx *e, const uint8_t *bgra, int stride) {
    vpx_image_t *im = e->img;
    rc_bgra_to_i444(bgra, stride, (int)im->d_w, (int)im->d_h, im->planes[0], im->stride[0], im->planes[1],
                    im->stride[1], im->planes[2], im->stride[2]);
}

/* H.264 QP 0..51 onto libvpx's 0..63 quantizer scale. */
static int vpx_q(int qp) { return clampi((qp * 63 + 25) / 51, RC_VPX_MIN_Q, RC_VPX_MAX_Q); }

int rc_vpx_encode(rc_vpx *e, int64_t pts_us, int key, int max_qp, int32_t flags, rc_frame_fn emit, void *user) {
    if (!e) return RC_ERR;
    if (max_qp > 0 || e->clamped) {
        vpx_codec_enc_cfg_t c = e->cfg;
        if (max_qp > 0) {
            c.rc_max_quantizer = (unsigned)vpx_q(max_qp);
            if (c.rc_min_quantizer > c.rc_max_quantizer) c.rc_min_quantizer = c.rc_max_quantizer;
        }
        if (vpx_codec_enc_config_set(&e->codec, &c) != VPX_CODEC_OK) return RC_ERR;
        e->clamped = max_qp > 0;
    }
    vpx_codec_err_t err = vpx_codec_encode(&e->codec, e->img, pts_us, (unsigned long)(1000000 / e->fps),
                                           key ? VPX_EFLAG_FORCE_KF : 0, VPX_DL_REALTIME);
    if (err != VPX_CODEC_OK) return RC_ERR;
    vpx_codec_iter_t it = NULL;
    const vpx_codec_cx_pkt_t *pkt;
    while ((pkt = vpx_codec_get_cx_data(&e->codec, &it)) != NULL) {
        if (pkt->kind != VPX_CODEC_CX_FRAME_PKT || pkt->data.frame.sz == 0) continue;
        int32_t fl = flags | ((pkt->data.frame.flags & VPX_FRAME_IS_KEY) ? RC_FRAME_KEY : 0);
        if (emit) emit(user, pkt->data.frame.buf, (int32_t)pkt->data.frame.sz, pts_us, fl);
    }
    return RC_OK;
}
