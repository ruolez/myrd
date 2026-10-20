// Cursor stream: polls GetCursorInfo at ~120 Hz, sends position changes
// normalized to the captured monitor, and the cursor image (RGBA) when the
// cursor handle changes. Never composited into the video.
#include "rc_win.h"

#include <mmsystem.h>
#include <stdlib.h>

namespace rcw {

struct CursorState {
    RECT rect;
    rc_cursor_shape_fn on_shape;
    rc_cursor_pos_fn on_pos;
    void *user;
    HANDLE thread;
    volatile LONG stop;
};

static CursorState *g_cursor = nullptr;

static uint32_t fnv1a(const uint8_t *p, size_t n, uint32_t h = 2166136261u) {
    for (size_t i = 0; i < n; i++) {
        h ^= p[i];
        h *= 16777619u;
    }
    return h;
}

// Reads a bitmap as top-down 32-bit BGRA rows.
static bool read_bits(HDC dc, HBITMAP bmp, int w, int h, uint8_t *out) {
    BITMAPINFO bi = {};
    bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bi.bmiHeader.biWidth = w;
    bi.bmiHeader.biHeight = -h;
    bi.bmiHeader.biPlanes = 1;
    bi.bmiHeader.biBitCount = 32;
    bi.bmiHeader.biCompression = BI_RGB;
    return GetDIBits(dc, bmp, 0, (UINT)h, out, &bi, DIB_RGB_COLORS) == h;
}

// Converts an HCURSOR to RGBA. Returns false if the cursor has no image.
static bool cursor_rgba(HCURSOR cur, uint8_t **rgba, int *w, int *h, int *hx, int *hy) {
    ICONINFO ii;
    if (!GetIconInfo(cur, &ii)) return false;
    BITMAP mb = {};
    GetObject(ii.hbmMask, sizeof(mb), &mb);
    bool mono = ii.hbmColor == nullptr;
    int width = mb.bmWidth;
    int height = mono ? mb.bmHeight / 2 : mb.bmHeight;
    if (width <= 0 || height <= 0 || width > 256 || height > 256) {
        if (ii.hbmColor) DeleteObject(ii.hbmColor);
        DeleteObject(ii.hbmMask);
        return false;
    }
    HDC dc = GetDC(nullptr);
    size_t px = (size_t)width * height;
    uint8_t *mask = (uint8_t *)malloc(px * 4 * (mono ? 2 : 1));
    uint8_t *color = mono ? nullptr : (uint8_t *)malloc(px * 4);
    uint8_t *outp = (uint8_t *)malloc(px * 4);
    bool ok = mask && outp && (mono || color);
    if (ok) ok = read_bits(dc, ii.hbmMask, width, mono ? height * 2 : height, mask);
    if (ok && !mono) ok = read_bits(dc, ii.hbmColor, width, height, color);
    ReleaseDC(nullptr, dc);

    if (ok) {
        if (mono) {
            // Top half AND mask, bottom half XOR mask.
            for (size_t i = 0; i < px; i++) {
                bool a = mask[i * 4] != 0;
                bool x = mask[(px + i) * 4] != 0;
                uint8_t *o = outp + i * 4;
                if (a && !x) { o[0] = o[1] = o[2] = 0; o[3] = 0; }          // transparent
                else if (!a && !x) { o[0] = o[1] = o[2] = 0; o[3] = 255; }  // black
                else if (!a && x) { o[0] = o[1] = o[2] = 255; o[3] = 255; } // white
                else { o[0] = o[1] = o[2] = 0; o[3] = 200; }                // inverted → dark
            }
        } else {
            bool has_alpha = false;
            for (size_t i = 0; i < px && !has_alpha; i++) has_alpha = color[i * 4 + 3] != 0;
            for (size_t i = 0; i < px; i++) {
                uint8_t *o = outp + i * 4;
                const uint8_t *s = color + i * 4;
                o[0] = s[2];
                o[1] = s[1];
                o[2] = s[0];
                o[3] = has_alpha ? s[3] : (mask[i * 4] ? 0 : 255);
            }
        }
        *rgba = outp;
        *w = width;
        *h = height;
        *hx = (int)ii.xHotspot;
        *hy = (int)ii.yHotspot;
    } else {
        free(outp);
    }
    free(mask);
    free(color);
    if (ii.hbmColor) DeleteObject(ii.hbmColor);
    DeleteObject(ii.hbmMask);
    return ok;
}

static DWORD WINAPI cursor_thread(void *p) {
    CursorState *s = (CursorState *)p;
    timeBeginPeriod(1);
    HCURSOR last_cur = nullptr;
    uint32_t last_id = 0;
    int last_x = -1, last_y = -1, last_vis = -1;
    LONG rw = s->rect.right - s->rect.left;
    LONG rh = s->rect.bottom - s->rect.top;

    while (!s->stop) {
        attach_input_desktop();
        CURSORINFO ci;
        ci.cbSize = sizeof(ci);
        if (GetCursorInfo(&ci)) {
            bool showing = (ci.flags & CURSOR_SHOWING) != 0 && ci.hCursor;
            POINT pt = ci.ptScreenPos;
            bool inside = pt.x >= s->rect.left && pt.x < s->rect.right && pt.y >= s->rect.top && pt.y < s->rect.bottom;
            int vis = (showing && inside) ? 1 : 0;

            if (showing && ci.hCursor != last_cur) {
                last_cur = ci.hCursor;
                uint8_t *rgba = nullptr;
                int w = 0, h = 0, hx = 0, hy = 0;
                if (cursor_rgba(ci.hCursor, &rgba, &w, &h, &hx, &hy)) {
                    uint32_t id = fnv1a(rgba, (size_t)w * h * 4);
                    uint16_t dims[4] = {(uint16_t)w, (uint16_t)h, (uint16_t)hx, (uint16_t)hy};
                    id = fnv1a((const uint8_t *)dims, sizeof(dims), id);
                    if (id == 0) id = 1;
                    if (id != last_id) {
                        last_id = id;
                        s->on_shape(s->user, id, (uint16_t)w, (uint16_t)h, (uint16_t)hx, (uint16_t)hy, rgba,
                                    w * h * 4);
                    }
                    free(rgba);
                }
            }

            LONG cx = pt.x - s->rect.left;
            LONG cy = pt.y - s->rect.top;
            if (cx < 0) cx = 0;
            if (cy < 0) cy = 0;
            if (cx > rw - 1) cx = rw - 1;
            if (cy > rh - 1) cy = rh - 1;
            int nx = rw > 1 ? (int)((int64_t)cx * 65535 / (rw - 1)) : 0;
            int ny = rh > 1 ? (int)((int64_t)cy * 65535 / (rh - 1)) : 0;
            if (nx != last_x || ny != last_y || vis != last_vis) {
                last_x = nx;
                last_y = ny;
                last_vis = vis;
                s->on_pos(s->user, (uint16_t)nx, (uint16_t)ny, (uint8_t)vis);
            }
        }
        Sleep(8);
    }
    timeEndPeriod(1);
    return 0;
}

void cursor_start(RECT rect, rc_cursor_shape_fn on_shape, rc_cursor_pos_fn on_pos, void *user) {
    cursor_stop();
    CursorState *s = new CursorState();
    s->rect = rect;
    s->on_shape = on_shape;
    s->on_pos = on_pos;
    s->user = user;
    s->stop = 0;
    s->thread = CreateThread(nullptr, 0, cursor_thread, s, 0, nullptr);
    if (!s->thread) {
        delete s;
        return;
    }
    g_cursor = s;
}

void cursor_stop() {
    CursorState *s = g_cursor;
    g_cursor = nullptr;
    if (!s) return;
    InterlockedExchange(&s->stop, 1);
    WaitForSingleObject(s->thread, INFINITE);
    CloseHandle(s->thread);
    delete s;
}

} // namespace rcw
