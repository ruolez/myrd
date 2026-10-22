/*
 * rc_linux.c — rc_native.h over X11 for the Docker demo device.
 *
 *   capture : XShm on the root window, XDamage so a static screen costs ~0
 *   encode  : libswscale BGRX→I420 (BT.709) + libx264 superfast/zerolatency;
 *             full color: libswscale BGRX→I444 + rc_vpx (VP9 profile 1);
 *             refinement passes force the frame quantizer
 *   cursor  : XFixes cursor image + XQueryPointer polling at 120 Hz
 *   input   : XTest; text via a temporary keysym remap of a spare keycode
 *   clipboard: xclip subprocess
 *   indicator: four red override-redirect windows around each monitor
 *
 * Threads: each worker opens its own Display connection; the shared state
 * below is guarded by g.mu.
 */
#define _GNU_SOURCE
#include "rc_native.h"

#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/keysym.h>
#include <X11/extensions/XShm.h>
#include <X11/extensions/XTest.h>
#include <X11/extensions/Xdamage.h>
#include <X11/extensions/Xfixes.h>
#include <X11/extensions/Xrandr.h>
#include <libswscale/swscale.h>
#include <libavutil/pixfmt.h>
#include <x264.h>

#include "rc_scale.h"
#include "rc_vpx.h"

#include <errno.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ipc.h>
#include <sys/shm.h>
#include <time.h>
#include <unistd.h>

#define MAX_MONITORS 16
#define WHEEL_NOTCH_PX 100
#define IDLE_FRAME_NS 1000000000LL /* at least one frame per second on a static screen */
#define REFINE_QUIET_NS 100000000LL /* rc_encode_refine: no new content for this long */
#define MINOR_ROWS_PER 50            /* at most 1/50 of the rows changed: RC_FRAME_MINOR */

typedef struct {
    pthread_mutex_t mu;
    int inited;
    Display *dpy;       /* control connection: monitors, indicator */
    Display *in_dpy;    /* input connection (guarded by in_mu) */
    pthread_mutex_t in_mu;
    pthread_mutex_t border_mu; /* borders are raised from the cursor thread */
    int screen_w, screen_h;
    rc_monitor monitors[MAX_MONITORS];
    int n_monitors;
    KeyCode spare_keycode;
    int wheel_acc_x, wheel_acc_y;

    /* capture */
    pthread_t cap_thread, cur_thread;
    atomic_int running;
    atomic_int want_keyframe;
    atomic_int new_bitrate; /* kbps, 0 = no change */
    atomic_int new_fps;
    atomic_int refine_qp; /* 0 = none pending, -1 = a repeat pass */
    rc_capture_config cfg;
    rc_monitor cap_mon;
    rc_frame_fn on_frame;
    rc_cursor_shape_fn on_shape;
    rc_cursor_pos_fn on_pos;
    void *user;

    /* indicator */
    Window border[MAX_MONITORS][4];
    int border_n;
    atomic_int indicator_on;
} rc_state;

static rc_state g = {.mu = PTHREAD_MUTEX_INITIALIZER, .in_mu = PTHREAD_MUTEX_INITIALIZER, .border_mu = PTHREAD_MUTEX_INITIALIZER};

static int x_error_handler(Display *d, XErrorEvent *e) {
    char buf[128];
    XGetErrorText(d, e->error_code, buf, sizeof buf);
    fprintf(stderr, "rc_linux: X error %s (request %d)\n", buf, e->request_code);
    return 0;
}

static int64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static void sleep_until(int64_t t) {
    struct timespec ts = {.tv_sec = t / 1000000000LL, .tv_nsec = t % 1000000000LL};
    while (clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME, &ts, NULL) == EINTR) {
    }
}

static Display *open_display(void) {
    const char *name = getenv("DISPLAY");
    return XOpenDisplay(name && *name ? name : ":0");
}

/* ---------------- monitors ---------------- */

static void refresh_monitors_locked(void) {
    int n = 0;
    Window root = DefaultRootWindow(g.dpy);
    g.screen_w = DisplayWidth(g.dpy, DefaultScreen(g.dpy));
    g.screen_h = DisplayHeight(g.dpy, DefaultScreen(g.dpy));
    int ev, er;
    if (XRRQueryExtension(g.dpy, &ev, &er)) {
        int cnt = 0;
        XRRMonitorInfo *mi = XRRGetMonitors(g.dpy, root, True, &cnt);
        for (int i = 0; mi && i < cnt && n < MAX_MONITORS; i++) {
            if (mi[i].width <= 0 || mi[i].height <= 0) continue;
            rc_monitor *m = &g.monitors[n];
            memset(m, 0, sizeof *m);
            m->id = (uint32_t)(n + 1);
            m->x = mi[i].x;
            m->y = mi[i].y;
            m->width = (uint32_t)mi[i].width;
            m->height = (uint32_t)mi[i].height;
            m->scale = 1.0f;
            m->primary = mi[i].primary ? 1 : 0;
            char *nm = mi[i].name ? XGetAtomName(g.dpy, mi[i].name) : NULL;
            snprintf(m->name, sizeof m->name, "%s", nm ? nm : "Display");
            if (nm) XFree(nm);
            n++;
        }
        if (mi) XRRFreeMonitors(mi);
    }
    if (n == 0) {
        rc_monitor *m = &g.monitors[0];
        memset(m, 0, sizeof *m);
        m->id = 1;
        m->width = (uint32_t)g.screen_w;
        m->height = (uint32_t)g.screen_h;
        m->scale = 1.0f;
        m->primary = 1;
        snprintf(m->name, sizeof m->name, "Screen %dx%d", g.screen_w, g.screen_h);
        n = 1;
    }
    int has_primary = 0;
    for (int i = 0; i < n; i++) has_primary |= g.monitors[i].primary;
    if (!has_primary) g.monitors[0].primary = 1;
    g.n_monitors = n;
}

static int find_monitor_locked(uint32_t id, rc_monitor *out) {
    for (int i = 0; i < g.n_monitors; i++) {
        if (g.monitors[i].id == id || (id == 0 && g.monitors[i].primary)) {
            *out = g.monitors[i];
            return 1;
        }
    }
    if (g.n_monitors > 0) {
        *out = g.monitors[0];
        return id == 0;
    }
    return 0;
}

/* ---------------- lifecycle ---------------- */

static KeyCode find_spare_keycode(Display *d) {
    int minkc, maxkc, per;
    XDisplayKeycodes(d, &minkc, &maxkc);
    KeySym *map = XGetKeyboardMapping(d, (KeyCode)minkc, maxkc - minkc + 1, &per);
    KeyCode spare = 0;
    for (int kc = maxkc; kc >= minkc && map; kc--) {
        int empty = 1;
        for (int j = 0; j < per; j++)
            if (map[(kc - minkc) * per + j] != NoSymbol) empty = 0;
        if (empty) {
            spare = (KeyCode)kc;
            break;
        }
    }
    if (map) XFree(map);
    return spare;
}

int rc_init(void) {
    pthread_mutex_lock(&g.mu);
    if (g.inited) {
        pthread_mutex_unlock(&g.mu);
        return RC_OK;
    }
    XInitThreads();
    XSetErrorHandler(x_error_handler);
    g.dpy = open_display();
    g.in_dpy = open_display();
    if (!g.dpy || !g.in_dpy) {
        pthread_mutex_unlock(&g.mu);
        fprintf(stderr, "rc_linux: cannot open X display\n");
        return RC_ERR;
    }
    int a, b, c, d;
    if (!XShmQueryExtension(g.dpy) || !XTestQueryExtension(g.in_dpy, &a, &b, &c, &d) ||
        !XFixesQueryExtension(g.dpy, &a, &b)) {
        pthread_mutex_unlock(&g.mu);
        fprintf(stderr, "rc_linux: X server lacks MIT-SHM, XTEST or XFIXES\n");
        return RC_ERR_UNSUPPORTED;
    }
    refresh_monitors_locked();
    g.spare_keycode = find_spare_keycode(g.in_dpy);
    g.inited = 1;
    pthread_mutex_unlock(&g.mu);
    return RC_OK;
}

void rc_shutdown(void) {
    rc_capture_stop();
    rc_indicator_show(0);
    pthread_mutex_lock(&g.mu);
    if (g.dpy) XCloseDisplay(g.dpy);
    if (g.in_dpy) XCloseDisplay(g.in_dpy);
    g.dpy = g.in_dpy = NULL;
    g.inited = 0;
    pthread_mutex_unlock(&g.mu);
}

const char *rc_backend_name(void) { return "linux-x11"; }
const char *rc_encoder_name(void) {
    return atomic_load(&g.running) && g.cfg.codec == RC_CODEC_VP9 ? "libvpx-vp9" : "x264";
}

int rc_codecs(void) { return 1 << RC_CODEC_H264 | 1 << RC_CODEC_VP9; }

int rc_list_monitors(rc_monitor *out, int max) {
    pthread_mutex_lock(&g.mu);
    if (!g.inited) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    refresh_monitors_locked();
    int n = g.n_monitors < max ? g.n_monitors : max;
    memcpy(out, g.monitors, (size_t)n * sizeof(rc_monitor));
    pthread_mutex_unlock(&g.mu);
    return n;
}

int rc_check_permissions(int *screen, int *input) {
    int ok = g.inited ? 1 : 0;
    if (screen) *screen = ok;
    if (input) *input = ok;
    return RC_OK;
}

void rc_request_permission(int which) { (void)which; }

/* ---------------- encoder ---------------- */

typedef struct {
    x264_t *enc;
    x264_param_t param;
    x264_picture_t pic;
    rc_vpx *vpx; /* full color instead of x264 */
    uint8_t *planes[3];
    int strides[3];
    struct SwsContext *sws;
    int out_w, out_h, kbps;
    int64_t frame_no;
} encoder;

static void apply_rate(x264_param_t *p, int kbps, int fps) {
    if (kbps < 200) kbps = 200;
    if (fps < 1) fps = 1;
    if (fps > 60) fps = 60;
    p->rc.i_rc_method = X264_RC_ABR;
    p->rc.i_bitrate = kbps;
    p->rc.i_vbv_max_bitrate = kbps;
    /* ~2 frames of VBV: tight enough for low latency, loose enough for text edits */
    p->rc.i_vbv_buffer_size = kbps * 2 / fps > 50 ? kbps * 2 / fps : 50;
    p->i_fps_num = (uint32_t)fps;
    p->i_fps_den = 1;
}

static int sws_open(encoder *e, int in_w, int in_h, enum AVPixelFormat out) {
    e->sws = sws_getContext(in_w, in_h, AV_PIX_FMT_BGR0, e->out_w, e->out_h, out,
                            (in_w == e->out_w && in_h == e->out_h) ? SWS_POINT : SWS_BILINEAR, NULL, NULL, NULL);
    if (!e->sws) return RC_ERR;
    sws_setColorspaceDetails(e->sws, sws_getCoefficients(SWS_CS_DEFAULT), 1, sws_getCoefficients(SWS_CS_ITU709), 0, 0,
                             1 << 16, 1 << 16);
    return RC_OK;
}

static int encoder_open(encoder *e, int in_w, int in_h, int out_w, int out_h, const rc_capture_config *cfg) {
    memset(e, 0, sizeof *e);
    e->out_w = out_w;
    e->out_h = out_h;
    e->kbps = cfg->bitrate_kbps > 0 ? cfg->bitrate_kbps : 3000;
    if (cfg->codec == RC_CODEC_VP9) {
        e->vpx = rc_vpx_open(out_w, out_h, cfg->fps > 0 ? cfg->fps : 30, e->kbps);
        if (!e->vpx) return RC_ERR;
        rc_vpx_planes(e->vpx, e->planes, e->strides);
        if (sws_open(e, in_w, in_h, AV_PIX_FMT_YUV444P) != RC_OK) {
            rc_vpx_close(e->vpx);
            return RC_ERR;
        }
        return RC_OK;
    }
    x264_param_t *p = &e->param;
    /* superfast turns on the deblocking, intra 4x4/8x8 analysis and sub-pixel
     * motion search that ultrafast skips: cleaner text for ~2x the CPU. */
    if (x264_param_default_preset(p, "superfast", "zerolatency") < 0) return RC_ERR;
    p->i_width = out_w;
    p->i_height = out_h;
    p->i_csp = X264_CSP_I420;
    p->i_log_level = X264_LOG_WARNING;
    p->b_repeat_headers = 1;
    p->b_annexb = 1;
    p->i_bframe = 0;
    p->b_vfr_input = 0;
    p->i_threads = 4;
    p->vui.i_colorprim = 1; /* BT.709 */
    p->vui.i_transfer = 1;
    p->vui.i_colmatrix = 1;
    p->vui.b_fullrange = 0;
    int fps = cfg->fps > 0 ? cfg->fps : 30;
    int gop_ms = cfg->keyframe_interval_ms > 0 ? cfg->keyframe_interval_ms : 2000;
    p->i_keyint_max = fps * gop_ms / 1000;
    if (p->i_keyint_max < 1) p->i_keyint_max = 1;
    p->i_keyint_min = 1;
    p->i_scenecut_threshold = 0;
    apply_rate(p, cfg->bitrate_kbps > 0 ? cfg->bitrate_kbps : 3000, fps);
    const char *profile = cfg->profile_idc >= 100 ? "high" : cfg->profile_idc >= 77 ? "main" : "baseline";
    if (x264_param_apply_profile(p, profile) < 0) return RC_ERR;
    e->enc = x264_encoder_open(p);
    if (!e->enc) return RC_ERR;
    if (x264_picture_alloc(&e->pic, X264_CSP_I420, out_w, out_h) < 0) {
        x264_encoder_close(e->enc);
        return RC_ERR;
    }
    for (int i = 0; i < 3; i++) {
        e->planes[i] = e->pic.img.plane[i];
        e->strides[i] = e->pic.img.i_stride[i];
    }
    if (sws_open(e, in_w, in_h, AV_PIX_FMT_YUV420P) != RC_OK) {
        x264_picture_clean(&e->pic);
        x264_encoder_close(e->enc);
        return RC_ERR;
    }
    return RC_OK;
}

static void encoder_rate(encoder *e, int kbps, int fps) {
    if (kbps) e->kbps = kbps;
    if (e->vpx) {
        rc_vpx_rate(e->vpx, e->kbps, fps);
        return;
    }
    apply_rate(&e->param, e->kbps, fps);
    x264_encoder_reconfig(e->enc, &e->param);
}

static void encoder_close(encoder *e) {
    if (e->sws) sws_freeContext(e->sws);
    rc_vpx_close(e->vpx);
    if (e->enc) {
        x264_picture_clean(&e->pic);
        x264_encoder_close(e->enc);
    }
    memset(e, 0, sizeof *e);
}

/* ---------------- capture thread ---------------- */

/* Encodes the current picture; qp > 0 forces that quantizer (a refinement pass). */
static void encode_emit(encoder *e, int64_t t, int key, int qp, int flags) {
    if (e->vpx) {
        rc_vpx_encode(e->vpx, t / 1000, key, qp, flags, g.on_frame, g.user);
        return;
    }
    e->pic.i_type = key ? X264_TYPE_IDR : X264_TYPE_AUTO;
    e->pic.i_qpplus1 = qp > 0 ? qp + 1 : X264_QP_AUTO;
    e->pic.i_pts = e->frame_no++;
    x264_nal_t *nals;
    int n_nals;
    x264_picture_t out;
    int size = x264_encoder_encode(e->enc, &nals, &n_nals, &e->pic, &out);
    if (size > 0 && g.on_frame) {
        if (out.b_keyframe) flags |= RC_FRAME_KEY;
        /* x264 lays out all NAL payloads of a frame contiguously */
        g.on_frame(g.user, nals[0].p_payload, size, t / 1000, flags);
    }
}

static void compute_out_size(int w, int h, int max_w, int max_h, int *ow, int *oh) {
    double s = 1.0;
    if (max_w > 0 && w > max_w) s = (double)max_w / w;
    if (max_h > 0 && h * s > max_h) s = (double)max_h / h;
    *ow = ((int)(w * s + 0.5)) & ~1; /* nearest: a bound the agent sized exactly stays exact */
    *oh = ((int)(h * s + 0.5)) & ~1;
}

/* X reports damage that changes no pixel (a window raised over its own
 * contents, a repainted wallpaper): rows that differ from the last capture. */
static int changed_rows(const uint8_t *a, const uint8_t *b, size_t stride, int rows) {
    int n = 0;
    for (int r = 0; r < rows; r++)
        if (memcmp(a + (size_t)r * stride, b + (size_t)r * stride, stride) != 0) n++;
    return n;
}

static void *capture_main(void *arg) {
    (void)arg;
    Display *d = open_display();
    if (!d) {
        fprintf(stderr, "rc_linux: capture thread cannot open display\n");
        return NULL;
    }
    Window root = DefaultRootWindow(d);
    int scr = DefaultScreen(d);
    rc_monitor mon = g.cap_mon;
    int cw = (int)mon.width & ~1, ch = (int)mon.height & ~1;
    int ow, oh;
    compute_out_size(cw, ch, g.cfg.max_width, g.cfg.max_height, &ow, &oh);

    XShmSegmentInfo shm = {0};
    XImage *img = XShmCreateImage(d, DefaultVisual(d, scr), (unsigned)DefaultDepth(d, scr), ZPixmap, NULL, &shm,
                                  (unsigned)cw, (unsigned)ch);
    if (!img) {
        XCloseDisplay(d);
        return NULL;
    }
    shm.shmid = shmget(IPC_PRIVATE, (size_t)img->bytes_per_line * img->height, IPC_CREAT | 0600);
    shm.shmaddr = img->data = shmat(shm.shmid, NULL, 0);
    shm.readOnly = False;
    XShmAttach(d, &shm);
    XSync(d, False);
    shmctl(shm.shmid, IPC_RMID, NULL);

    int dmg_ev = 0, dmg_err = 0;
    Damage damage = 0;
    if (XDamageQueryExtension(d, &dmg_ev, &dmg_err)) damage = XDamageCreate(d, root, XDamageReportNonEmpty);

    encoder enc;
    int fps = g.cfg.fps > 0 ? g.cfg.fps : 30;
    if (encoder_open(&enc, cw, ch, ow, oh, &g.cfg) != RC_OK) {
        fprintf(stderr, "rc_linux: encoder open failed\n");
        goto out;
    }

    int64_t interval = 1000000000LL / fps;
    int64_t next = now_ns();
    int64_t last_emit = 0, last_content = 0;
    int damaged = 1;
    size_t frame_bytes = (size_t)img->bytes_per_line * ch;
    uint8_t *prev = malloc(frame_bytes); /* the last captured pixels */
    int have_prev = 0;
    while (atomic_load(&g.running)) {
        int nb = atomic_exchange(&g.new_bitrate, 0);
        int nf = atomic_exchange(&g.new_fps, 0);
        if (nb || nf) {
            if (nf) {
                fps = nf;
                interval = 1000000000LL / fps;
            }
            encoder_rate(&enc, nb, fps);
        }

        while (XPending(d)) {
            XEvent ev;
            XNextEvent(d, &ev);
            if (damage && ev.type == dmg_ev + XDamageNotify) damaged = 1;
        }
        if (!damage) damaged = 1;
        int key = atomic_exchange(&g.want_keyframe, 0);
        int refine = atomic_exchange(&g.refine_qp, 0);
        int64_t t = now_ns();
        int captured = 0, changed = 0;
        if (damaged || key || t - last_emit >= IDLE_FRAME_NS) {
            if (damage) XDamageSubtract(d, damage, None, None);
            damaged = 0;
            if (XShmGetImage(d, root, img, mon.x, mon.y, AllPlanes)) {
                captured = 1;
                changed = have_prev && prev ? changed_rows((const uint8_t *)img->data, prev, img->bytes_per_line, ch) : ch;
                if (changed) {
                    if (prev) memcpy(prev, img->data, frame_bytes);
                    have_prev = 1;
                    last_content = t;
                    const uint8_t *src[1] = {(const uint8_t *)img->data};
                    int src_stride[1] = {img->bytes_per_line};
                    sws_scale(enc.sws, src, src_stride, 0, ch, enc.planes, enc.strides);
                }
            }
        }
        /* the encoder's input holds the last captured picture */
        if (captured && (changed || key)) {
            int flags = !changed ? RC_FRAME_STATIC : changed * MINOR_ROWS_PER <= ch ? RC_FRAME_MINOR : 0;
            encode_emit(&enc, t, key, 0, key ? 0 : flags);
            last_emit = t;
        } else if (refine && last_emit && t - last_content >= REFINE_QUIET_NS) {
            encode_emit(&enc, t, 0, refine > 0 ? refine : 0, RC_FRAME_STATIC);
            last_emit = t;
        } else if (captured && t - last_emit >= IDLE_FRAME_NS) {
            encode_emit(&enc, t, 0, 0, RC_FRAME_STATIC);
            last_emit = t;
        }
        next += interval;
        if (next < now_ns() - interval) next = now_ns(); /* fell behind: don't burst */
        sleep_until(next);
    }
    encoder_close(&enc);
    free(prev);
out:
    if (damage) XDamageDestroy(d, damage);
    XShmDetach(d, &shm);
    XDestroyImage(img);
    shmdt(shm.shmaddr);
    XCloseDisplay(d);
    return NULL;
}

/* ---------------- cursor thread ---------------- */

static void emit_cursor_shape(Display *d) {
    XFixesCursorImage *ci = XFixesGetCursorImage(d);
    if (!ci) return;
    int w = ci->width, h = ci->height;
    if (w > 0 && h > 0 && w <= 256 && h <= 256 && g.on_shape) {
        uint8_t *rgba = malloc((size_t)w * h * 4);
        if (rgba) {
            for (int i = 0; i < w * h; i++) {
                unsigned long p = ci->pixels[i]; /* premultiplied ARGB in the low 32 bits */
                uint8_t a = (p >> 24) & 0xff, r = (p >> 16) & 0xff, gg = (p >> 8) & 0xff, b = p & 0xff;
                if (a && a < 255) {
                    r = (uint8_t)(r * 255 / a);
                    gg = (uint8_t)(gg * 255 / a);
                    b = (uint8_t)(b * 255 / a);
                }
                rgba[i * 4] = r;
                rgba[i * 4 + 1] = gg;
                rgba[i * 4 + 2] = b;
                rgba[i * 4 + 3] = a;
            }
            g.on_shape(g.user, (uint32_t)ci->cursor_serial, (uint16_t)w, (uint16_t)h, (uint16_t)ci->xhot,
                       (uint16_t)ci->yhot, rgba, w * h * 4);
            free(rgba);
        }
    }
    XFree(ci);
}

static void raise_borders(Display *d) {
    if (!atomic_load(&g.indicator_on)) return;
    pthread_mutex_lock(&g.border_mu);
    for (int i = 0; i < g.border_n; i++)
        for (int j = 0; j < 4; j++)
            if (g.border[i][j]) XRaiseWindow(d, g.border[i][j]);
    pthread_mutex_unlock(&g.border_mu);
    XFlush(d);
}

static void *cursor_main(void *arg) {
    (void)arg;
    Display *d = open_display();
    if (!d) return NULL;
    Window root = DefaultRootWindow(d);
    int fx_ev, fx_err;
    XFixesQueryExtension(d, &fx_ev, &fx_err);
    XFixesSelectCursorInput(d, root, XFixesDisplayCursorNotifyMask);
    emit_cursor_shape(d);
    rc_monitor mon = g.cap_mon;
    int last_x = -1, last_y = -1, last_vis = -1;
    int64_t next = now_ns(), last_raise = 0;
    while (atomic_load(&g.running)) {
        int shape_changed = 0;
        while (XPending(d)) {
            XEvent ev;
            XNextEvent(d, &ev);
            if (ev.type == fx_ev + XFixesCursorNotify) shape_changed = 1;
        }
        if (shape_changed) emit_cursor_shape(d);
        Window rr, cr;
        int rx, ry, wx, wy;
        unsigned int mask;
        if (XQueryPointer(d, root, &rr, &cr, &rx, &ry, &wx, &wy, &mask)) {
            int inside = rx >= mon.x && ry >= mon.y && rx < mon.x + (int)mon.width && ry < mon.y + (int)mon.height;
            int nx = 0, ny = 0;
            if (inside) {
                nx = (int)((int64_t)(rx - mon.x) * 65535 / (mon.width > 1 ? mon.width - 1 : 1));
                ny = (int)((int64_t)(ry - mon.y) * 65535 / (mon.height > 1 ? mon.height - 1 : 1));
            }
            if ((nx != last_x || ny != last_y || inside != last_vis) && g.on_pos) {
                g.on_pos(g.user, (uint16_t)nx, (uint16_t)ny, (uint8_t)inside);
                last_x = nx, last_y = ny, last_vis = inside;
            }
        }
        int64_t t = now_ns();
        if (t - last_raise > 1000000000LL) {
            raise_borders(d);
            last_raise = t;
        }
        next += 1000000000LL / 120;
        if (next < t) next = t;
        sleep_until(next);
    }
    XCloseDisplay(d);
    return NULL;
}

int rc_capture_start(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                     rc_cursor_pos_fn on_pos, void *user) {
    pthread_mutex_lock(&g.mu);
    if (!g.inited) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    if (atomic_load(&g.running)) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR_BUSY;
    }
    refresh_monitors_locked();
    rc_monitor mon;
    if (!find_monitor_locked(cfg->monitor_id, &mon)) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    g.cfg = *cfg;
    g.cap_mon = mon;
    g.on_frame = on_frame;
    g.on_shape = on_shape;
    g.on_pos = on_pos;
    g.user = user;
    atomic_store(&g.want_keyframe, 1);
    atomic_store(&g.new_bitrate, 0);
    atomic_store(&g.new_fps, 0);
    atomic_store(&g.refine_qp, 0);
    atomic_store(&g.running, 1);
    if (pthread_create(&g.cap_thread, NULL, capture_main, NULL) != 0) {
        atomic_store(&g.running, 0);
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    if (pthread_create(&g.cur_thread, NULL, cursor_main, NULL) != 0) {
        atomic_store(&g.running, 0);
        pthread_join(g.cap_thread, NULL);
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    pthread_mutex_unlock(&g.mu);
    return RC_OK;
}

/* ---------------- preview ---------------- */

/* Holds g.mu throughout, so rc_capture_start waits for it. */
int rc_snapshot(uint32_t monitor_id, int32_t max_w, int32_t max_h, rc_image *out) {
    memset(out, 0, sizeof *out);
    pthread_mutex_lock(&g.mu);
    if (!g.inited) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    if (atomic_load(&g.running)) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR_BUSY;
    }
    refresh_monitors_locked();
    rc_monitor mon;
    if (!find_monitor_locked(monitor_id, &mon)) {
        pthread_mutex_unlock(&g.mu);
        return RC_ERR;
    }
    XImage *img = XGetImage(g.dpy, DefaultRootWindow(g.dpy), mon.x, mon.y, mon.width, mon.height, AllPlanes, ZPixmap);
    pthread_mutex_unlock(&g.mu);
    if (!img) return RC_ERR;
    int rc = RC_ERR;
    if (img->bits_per_pixel == 32) {
        int32_t w, h;
        rc_fit((int32_t)mon.width, (int32_t)mon.height, max_w, max_h, &w, &h);
        out->pixels = malloc((size_t)w * h * 4);
        if (out->pixels) {
            rc_scale_bgra((const uint8_t *)img->data, (int32_t)mon.width, (int32_t)mon.height, img->bytes_per_line,
                          out->pixels, w, h);
            out->width = w;
            out->height = h;
            out->stride = w * 4;
            rc = RC_OK;
        }
    }
    XDestroyImage(img);
    return rc;
}

void rc_capture_stop(void) {
    pthread_mutex_lock(&g.mu);
    if (!atomic_load(&g.running)) {
        pthread_mutex_unlock(&g.mu);
        return;
    }
    atomic_store(&g.running, 0);
    pthread_join(g.cap_thread, NULL);
    pthread_join(g.cur_thread, NULL);
    g.on_frame = NULL;
    g.on_shape = NULL;
    g.on_pos = NULL;
    pthread_mutex_unlock(&g.mu);
}

void rc_encode_configure(int32_t bitrate_kbps, int32_t fps) {
    if (bitrate_kbps > 0) atomic_store(&g.new_bitrate, bitrate_kbps);
    if (fps > 0) atomic_store(&g.new_fps, fps > 60 ? 60 : fps);
}

void rc_request_keyframe(void) { atomic_store(&g.want_keyframe, 1); }

int rc_encode_refine(int32_t max_qp) {
    if (!atomic_load(&g.running)) return RC_ERR;
    atomic_store(&g.refine_qp, max_qp <= 0 ? -1 : max_qp > 51 ? 51 : max_qp);
    return RC_OK;
}

/* ---------------- input ---------------- */

static unsigned int x_button(uint8_t b) {
    switch (b) {
    case RC_BTN_LEFT: return 1;
    case RC_BTN_MIDDLE: return 2;
    case RC_BTN_RIGHT: return 3;
    case RC_BTN_BACK: return 8;
    case RC_BTN_FORWARD: return 9;
    }
    return 0;
}

static void click(Display *d, unsigned int b) {
    XTestFakeButtonEvent(d, b, True, CurrentTime);
    XTestFakeButtonEvent(d, b, False, CurrentTime);
}

int rc_input_inject(const rc_input_event *ev) {
    if (!g.inited) return RC_ERR;
    int rc = RC_OK;
    pthread_mutex_lock(&g.in_mu);
    Display *d = g.in_dpy;
    switch (ev->type) {
    case RC_EV_MOUSE_MOVE: {
        rc_monitor m;
        pthread_mutex_lock(&g.mu);
        int ok = find_monitor_locked(ev->monitor_id, &m);
        pthread_mutex_unlock(&g.mu);
        if (!ok) {
            rc = RC_ERR;
            break;
        }
        int px = m.x + (int)((int64_t)ev->x * (m.width > 1 ? m.width - 1 : 1) / 65535);
        int py = m.y + (int)((int64_t)ev->y * (m.height > 1 ? m.height - 1 : 1) / 65535);
        XTestFakeMotionEvent(d, -1, px, py, CurrentTime);
        break;
    }
    case RC_EV_MOUSE_MOVE_REL:
        XTestFakeRelativeMotionEvent(d, ev->dx, ev->dy, CurrentTime);
        break;
    case RC_EV_MOUSE_BUTTON: {
        unsigned int b = x_button(ev->button);
        if (!b) {
            rc = RC_ERR;
            break;
        }
        XTestFakeButtonEvent(d, b, ev->down ? True : False, CurrentTime);
        break;
    }
    case RC_EV_MOUSE_WHEEL:
        g.wheel_acc_y += ev->dy;
        g.wheel_acc_x += ev->dx;
        while (g.wheel_acc_y >= WHEEL_NOTCH_PX) click(d, 5), g.wheel_acc_y -= WHEEL_NOTCH_PX;
        while (g.wheel_acc_y <= -WHEEL_NOTCH_PX) click(d, 4), g.wheel_acc_y += WHEEL_NOTCH_PX;
        while (g.wheel_acc_x >= WHEEL_NOTCH_PX) click(d, 7), g.wheel_acc_x -= WHEEL_NOTCH_PX;
        while (g.wheel_acc_x <= -WHEEL_NOTCH_PX) click(d, 6), g.wheel_acc_x += WHEEL_NOTCH_PX;
        break;
    case RC_EV_KEY:
        if (ev->keycode < 8 || ev->keycode > 255) {
            rc = RC_ERR;
            break;
        }
        XTestFakeKeyEvent(d, ev->keycode, ev->down ? True : False, CurrentTime);
        break;
    default:
        rc = RC_ERR;
    }
    XFlush(d);
    pthread_mutex_unlock(&g.in_mu);
    return rc;
}

/* Decodes one UTF-8 code point; returns bytes consumed or 0 on error. */
static int utf8_next(const unsigned char *s, int len, uint32_t *cp) {
    if (len <= 0) return 0;
    if (s[0] < 0x80) return *cp = s[0], 1;
    int n = (s[0] & 0xE0) == 0xC0 ? 2 : (s[0] & 0xF0) == 0xE0 ? 3 : (s[0] & 0xF8) == 0xF0 ? 4 : 0;
    if (!n || n > len) return 0;
    uint32_t v = s[0] & (0x7F >> n);
    for (int i = 1; i < n; i++) {
        if ((s[i] & 0xC0) != 0x80) return 0;
        v = (v << 6) | (s[i] & 0x3F);
    }
    *cp = v;
    return n;
}

int rc_input_text(const char *utf8, int32_t len) {
    if (!g.inited || !g.spare_keycode) return RC_ERR_UNSUPPORTED;
    pthread_mutex_lock(&g.in_mu);
    Display *d = g.in_dpy;
    const unsigned char *s = (const unsigned char *)utf8;
    int i = 0;
    while (i < len) {
        uint32_t cp;
        int n = utf8_next(s + i, len - i, &cp);
        if (!n) break;
        i += n;
        KeySym ks = cp < 0x100 ? (KeySym)cp : (KeySym)(0x01000000 | cp);
        if (cp == '\n') ks = XK_Return;
        if (cp == '\t') ks = XK_Tab;
        KeySym pair[2] = {ks, ks};
        XChangeKeyboardMapping(d, g.spare_keycode, 2, pair, 1);
        XSync(d, False);
        XTestFakeKeyEvent(d, g.spare_keycode, True, CurrentTime);
        XTestFakeKeyEvent(d, g.spare_keycode, False, CurrentTime);
        XSync(d, False);
        usleep(2000); /* let clients process MappingNotify before the next remap */
    }
    KeySym none[2] = {NoSymbol, NoSymbol};
    XChangeKeyboardMapping(d, g.spare_keycode, 2, none, 1);
    XFlush(d);
    pthread_mutex_unlock(&g.in_mu);
    return RC_OK;
}

/* ---------------- clipboard (xclip) ---------------- */

int rc_clipboard_get_text(char **out, int32_t *len) {
    *out = NULL;
    *len = 0;
    FILE *f = popen("xclip -selection clipboard -o -t UTF8_STRING 2>/dev/null", "r");
    if (!f) return RC_ERR;
    size_t cap = 4096, n = 0;
    char *buf = malloc(cap);
    if (!buf) {
        pclose(f);
        return RC_ERR;
    }
    size_t r;
    while ((r = fread(buf + n, 1, cap - n, f)) > 0) {
        n += r;
        if (n == cap) {
            if (cap >= (1u << 20)) break; /* 1 MiB cap */
            cap *= 2;
            char *nb = realloc(buf, cap);
            if (!nb) break;
            buf = nb;
        }
    }
    pclose(f);
    *out = buf;
    *len = (int32_t)n;
    return RC_OK;
}

int rc_clipboard_set_text(const char *utf8, int32_t len) {
    FILE *f = popen("xclip -selection clipboard -i >/dev/null 2>&1", "w");
    if (!f) return RC_ERR;
    fwrite(utf8, 1, (size_t)len, f);
    return pclose(f) == 0 ? RC_OK : RC_ERR;
}

void rc_free(void *p) { free(p); }

/* xfdesktop draws the wallpaper into an ordinary window XShm cannot leave out. */
int rc_wallpaper_hide(int hide) {
    (void)hide;
    return RC_ERR_UNSUPPORTED;
}

int rc_screen_locked(void) { return RC_ERR_UNSUPPORTED; }

/* ---------------- indicator ---------------- */

void rc_indicator_show(int on) {
    pthread_mutex_lock(&g.mu);
    if (!g.inited) {
        pthread_mutex_unlock(&g.mu);
        return;
    }
    Display *d = g.dpy;
    pthread_mutex_lock(&g.border_mu);
    for (int i = 0; i < g.border_n; i++)
        for (int j = 0; j < 4; j++)
            if (g.border[i][j]) XDestroyWindow(d, g.border[i][j]), g.border[i][j] = 0;
    g.border_n = 0;
    atomic_store(&g.indicator_on, on ? 1 : 0);
    if (on) {
        refresh_monitors_locked();
        int scr = DefaultScreen(d);
        XColor red;
        Colormap cm = DefaultColormap(d, scr);
        XParseColor(d, cm, "#d93025", &red);
        XAllocColor(d, cm, &red);
        XSetWindowAttributes a = {.override_redirect = True, .background_pixel = red.pixel};
        const int t = 4;
        for (int i = 0; i < g.n_monitors; i++) {
            rc_monitor *m = &g.monitors[i];
            int rects[4][4] = {{m->x, m->y, (int)m->width, t},
                               {m->x, m->y + (int)m->height - t, (int)m->width, t},
                               {m->x, m->y, t, (int)m->height},
                               {m->x + (int)m->width - t, m->y, t, (int)m->height}};
            for (int j = 0; j < 4; j++) {
                Window w = XCreateWindow(d, RootWindow(d, scr), rects[j][0], rects[j][1], (unsigned)rects[j][2],
                                         (unsigned)rects[j][3], 0, CopyFromParent, InputOutput, CopyFromParent,
                                         CWOverrideRedirect | CWBackPixel, &a);
                XMapRaised(d, w);
                g.border[i][j] = w;
            }
        }
        g.border_n = g.n_monitors;
    }
    pthread_mutex_unlock(&g.border_mu);
    XFlush(d);
    XSync(d, False);
    pthread_mutex_unlock(&g.mu);
}

int rc_consent_prompt(const char *title, const char *message, int32_t timeout_s) {
    (void)title, (void)message, (void)timeout_s;
    return RC_ERR_UNSUPPORTED; /* the Go device console prompts instead */
}
