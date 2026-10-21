/*
 * Tests the shared full-color encoder (rc_vpx.c) against libvpx's decoder:
 *   native/common/test/run.sh            (host: macOS or Linux)
 * Checks the BT.709 conversion, that the stream decodes as 4:4:4, that
 * refinement passes sharpen a static screen, keyframes on request, and
 * reports real-time speed at desktop sizes.
 */
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <vpx/vp8dx.h>
#include <vpx/vpx_decoder.h>

#include "rc_vpx.h"

static int failures;
#define CHECK(cond, ...)                \
    do {                                \
        if (cond) {                     \
            printf("ok   " __VA_ARGS__); \
        } else {                        \
            printf("FAIL " __VA_ARGS__); \
            failures++;                 \
        }                               \
        printf("\n");                   \
    } while (0)

static double now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec * 1e3 + ts.tv_nsec / 1e6;
}

static uint32_t rng = 12345;
static uint32_t rnd(void) {
    rng = rng * 1664525u + 1013904223u;
    return rng >> 8;
}

/* A desktop-like BGRA picture: white page, colored 1-2 px "glyph" strokes in
 * text rows, a blue title bar and a few flat colored panels. */
static void paint_screen(uint8_t *px, int w, int h, int scroll) {
    static const uint8_t ink[][3] = {{0, 0, 0}, {0, 0, 200}, {200, 0, 0}, {0, 140, 0}, {120, 0, 160}};
    for (int y = 0; y < h; y++)
        for (int x = 0; x < w; x++) {
            uint8_t *p = px + ((size_t)y * w + x) * 4;
            p[0] = p[1] = p[2] = 255;
            p[3] = 255;
            if (y < 28) p[0] = 200, p[1] = 110, p[2] = 30; /* BGR blue bar */
            else if (x > w - 220 && y > 60 && y < 260) p[0] = 60, p[1] = 200, p[2] = 240;
        }
    uint32_t saved = rng;
    rng = 777;
    for (int row = 40; row + 14 < h; row += 18) {
        int vrow = row - scroll;
        for (int cx = 8; cx + 8 < w - 230; cx += 8) {
            const uint8_t *c = ink[(rnd() >> 4) % 5];
            uint32_t bits = rnd();
            if ((bits & 7) == 0) continue; /* word gap */
            for (int s = 0; s < 6; s++) {
                int sx = cx + (int)(rnd() % 6), sy = vrow + 2 + (int)(rnd() % 10), len = 2 + (int)(rnd() % 6);
                int vert = rnd() & 1;
                for (int k = 0; k < len; k++) {
                    int x = sx + (vert ? 0 : k), y = sy + (vert ? k : 0);
                    if (x < 0 || y < 30 || x >= w || y >= h) continue;
                    uint8_t *p = px + ((size_t)y * w + x) * 4;
                    p[0] = c[2], p[1] = c[1], p[2] = c[0];
                }
            }
        }
    }
    rng = saved;
}

typedef struct {
    vpx_codec_ctx_t dec;
    double psnr[3];
    int frames, keys, last_size, total, bad_fmt;
    const uint8_t *ref[3];
    int ref_stride[3];
    int w, h;
} sink;

static double plane_psnr(const uint8_t *a, int as, const uint8_t *b, int bs, int w, int h) {
    double se = 0;
    for (int y = 0; y < h; y++)
        for (int x = 0; x < w; x++) {
            int d = a[(size_t)y * as + x] - b[(size_t)y * bs + x];
            se += d * d;
        }
    double mse = se / ((double)w * h);
    return mse == 0 ? 99 : 10 * log10(255.0 * 255.0 / mse);
}

static void on_frame(void *user, const uint8_t *data, int32_t len, int64_t pts, int32_t flags) {
    sink *s = user;
    s->frames++;
    s->last_size = len;
    s->total += len;
    if (flags & RC_FRAME_KEY) s->keys++;
    if (vpx_codec_decode(&s->dec, data, (unsigned)len, NULL, 0) != VPX_CODEC_OK) {
        s->bad_fmt++;
        return;
    }
    vpx_codec_iter_t it = NULL;
    vpx_image_t *im = vpx_codec_get_frame(&s->dec, &it);
    if (!im || im->fmt != VPX_IMG_FMT_I444 || (int)im->d_w != s->w || (int)im->d_h != s->h) {
        s->bad_fmt++;
        return;
    }
    for (int p = 0; p < 3; p++)
        s->psnr[p] = plane_psnr(im->planes[p], im->stride[p], s->ref[p], s->ref_stride[p], s->w, s->h);
}

static void sink_init(sink *s, rc_vpx *e, int w, int h) {
    memset(s, 0, sizeof *s);
    vpx_codec_dec_init(&s->dec, vpx_codec_vp9_dx(), NULL, 0);
    uint8_t *pl[3];
    int st[3];
    rc_vpx_planes(e, pl, st);
    for (int i = 0; i < 3; i++) s->ref[i] = pl[i], s->ref_stride[i] = st[i];
    s->w = w, s->h = h;
}

static void test_conversion(void) {
    /* BGRA in; expected Y, Cb, Cr (BT.709 limited range). */
    static const struct { uint8_t bgra[4]; int y, u, v; } cases[] = {
        {{255, 255, 255, 255}, 235, 128, 128},
        {{0, 0, 0, 255}, 16, 128, 128},
        {{0, 0, 255, 255}, 63, 102, 240}, /* red */
        {{0, 255, 0, 255}, 173, 42, 26},  /* green */
        {{255, 0, 0, 255}, 32, 240, 118}, /* blue */
        {{128, 128, 128, 255}, 126, 128, 128},
    };
    int bad = 0;
    for (size_t i = 0; i < sizeof cases / sizeof cases[0]; i++) {
        uint8_t y, u, v;
        rc_bgra_to_i444(cases[i].bgra, 4, 1, 1, &y, 1, &u, 1, &v, 1);
        if (abs(y - cases[i].y) > 1 || abs(u - cases[i].u) > 1 || abs(v - cases[i].v) > 1) {
            printf("     bgra %u,%u,%u → %u,%u,%u want %d,%d,%d\n", cases[i].bgra[0], cases[i].bgra[1],
                   cases[i].bgra[2], y, u, v, cases[i].y, cases[i].u, cases[i].v);
            bad++;
        }
    }
    CHECK(bad == 0, "BT.709 limited-range conversion of primaries, gray, black and white");
}

/* A static screen sent at a starved bitrate, then refined pass by pass. */
static void test_refinement(void) {
    const int w = 1280, h = 720, fps = 30;
    rc_vpx *e = rc_vpx_open(w, h, fps, 600);
    CHECK(e != NULL, "open %dx%d", w, h);
    if (!e) return;
    uint8_t *px = malloc((size_t)w * h * 4);
    paint_screen(px, w, h, 0);
    rc_vpx_from_bgra(e, px, w * 4);
    sink s;
    sink_init(&s, e, w, h);
    int64_t t = 0, step = 1000000 / fps;
    rc_vpx_encode(e, t, 1, 0, 0, on_frame, &s);
    CHECK(s.keys == 1 && s.bad_fmt == 0, "first frame is a 4:4:4 keyframe (%d bytes)", s.last_size);
    for (int i = 0; i < 5; i++) rc_vpx_encode(e, t += step, 0, 0, RC_FRAME_STATIC, on_frame, &s);
    double base = s.psnr[0], base_u = s.psnr[1];
    printf("     after 5 repeats: Y %.1f dB, U %.1f, V %.1f\n", s.psnr[0], s.psnr[1], s.psnr[2]);
    static const int qps[] = {36, 33, 30, 27, 24, 21, 18};
    int total = 0;
    for (size_t i = 0; i < sizeof qps / sizeof qps[0]; i++) {
        rc_vpx_encode(e, t += 100000, 0, qps[i], RC_FRAME_STATIC, on_frame, &s);
        total += s.last_size;
        printf("     refine qp %d: %6d bytes, Y %.1f dB, U %.1f, V %.1f\n", qps[i], s.last_size, s.psnr[0], s.psnr[1],
               s.psnr[2]);
    }
    CHECK(s.psnr[0] >= base + 6 && s.psnr[1] >= base_u + 6, "refinement sharpens the static screen (Y %.1f → %.1f dB)",
          base, s.psnr[0]);
    CHECK(s.psnr[0] >= 37 && s.psnr[1] >= 37 && s.psnr[2] >= 37, "refined colors are close to the source (%.1f/%.1f/%.1f dB)",
          s.psnr[0], s.psnr[1], s.psnr[2]);
    double refined = s.psnr[0];
    rc_vpx_encode(e, t += step, 0, 0, RC_FRAME_STATIC, on_frame, &s);
    CHECK(s.psnr[0] >= refined - 0.5, "a normal frame after refinement keeps it (%.1f dB, %d bytes)", s.psnr[0],
          s.last_size);
    /* A clock ticks: a 60x16 corner changes once a second. The frame must stay
     * small (the agent rewinds refinement only two passes for < 4 KB) and
     * leave the refined rest alone. */
    for (int tick = 0; tick < 5; tick++) {
        for (int y = 4; y < 20; y++)
            for (int x = w - 70; x < w - 10; x++) px[((size_t)y * w + x) * 4 + 1] = (uint8_t)(40 * tick + x);
        rc_vpx_from_bgra(e, px, w * 4);
        rc_vpx_encode(e, t += 1000000, 0, 0, 0, on_frame, &s);
        printf("     clock tick %d: %d bytes, Y %.1f dB\n", tick, s.last_size, s.psnr[0]);
    }
    CHECK(s.last_size < 4096, "a small change encodes small (%d bytes)", s.last_size);
    for (int i = 0; i < 3; i++) rc_vpx_encode(e, t += 1000000, 0, 0, RC_FRAME_STATIC, on_frame, &s);
    CHECK(s.last_size < 8192, "an idle repeat of the refined screen stays small (%d bytes)", s.last_size);
    CHECK(s.psnr[0] >= refined - 1.0, "small changes keep the refined screen (%.1f → %.1f dB)", refined, s.psnr[0]);
    rc_vpx_encode(e, t += step, 1, 0, 0, on_frame, &s);
    CHECK(s.keys == 2, "keyframe on request");
    CHECK(s.bad_fmt == 0, "every frame decoded as %dx%d 4:4:4", w, h);
    vpx_codec_destroy(&s.dec);
    rc_vpx_close(e);
    free(px);
    (void)total;
}

/* Scrolling text at a desktop size: ms per frame and the bitrate it keeps. */
static void test_speed(int w, int h, int kbps) {
    const int fps = 30, n = 60;
    rc_vpx *e = rc_vpx_open(w, h, fps, kbps);
    if (!e) {
        CHECK(0, "open %dx%d", w, h);
        return;
    }
    uint8_t *px = malloc((size_t)w * h * 4);
    sink s;
    sink_init(&s, e, w, h);
    double enc = 0, conv = 0;
    int steady = 0;
    for (int i = 0; i < n; i++) {
        paint_screen(px, w, h, i * 6);
        double t0 = now_ms();
        rc_vpx_from_bgra(e, px, w * 4);
        double t1 = now_ms();
        rc_vpx_encode(e, (int64_t)i * 1000000 / fps, i == 0, 0, 0, on_frame, &s);
        double t2 = now_ms();
        if (i >= 5) conv += t1 - t0, enc += t2 - t1, steady += s.last_size;
    }
    int m = n - 5;
    double kbps_out = (double)steady * 8 / 1000 / ((double)m / fps);
    printf("     %dx%d @%d kbps: convert %.1f ms + encode %.1f ms per frame, %.0f kbps after the keyframe, Y %.1f dB\n", w, h, kbps,
           conv / m, enc / m, kbps_out, s.psnr[0]);
    CHECK(s.bad_fmt == 0 && s.frames == n, "%dx%d scrolling: %d frames decoded", w, h, s.frames);
    vpx_codec_destroy(&s.dec);
    rc_vpx_close(e);
    free(px);
}

int main(void) {
    test_conversion();
    test_refinement();
    test_speed(1920, 1080, 8000);
    test_speed(2560, 1440, 12000);
    test_speed(3840, 2160, 20000);
    printf(failures ? "%d FAILED\n" : "all passed\n", failures);
    return failures ? 1 : 0;
}
