#include "rc_scale.h"

void rc_fit(int32_t w, int32_t h, int32_t max_w, int32_t max_h, int32_t *out_w, int32_t *out_h) {
    int32_t ow = w, oh = h;
    if (w <= 0 || h <= 0 || max_w <= 0 || max_h <= 0) {
        *out_w = *out_h = 1;
        return;
    }
    if (ow > max_w) {
        oh = (int32_t)((int64_t)oh * max_w / ow);
        ow = max_w;
    }
    if (oh > max_h) {
        ow = (int32_t)((int64_t)ow * max_h / oh);
        oh = max_h;
    }
    *out_w = ow < 1 ? 1 : ow;
    *out_h = oh < 1 ? 1 : oh;
}

/* Each destination pixel averages the source pixels whose integer spans
 * [x0, x1) × [y0, y1) map onto it; spans tile the source exactly. */
void rc_scale_bgra(const uint8_t *src, int32_t src_w, int32_t src_h, int32_t src_stride,
                   uint8_t *dst, int32_t dst_w, int32_t dst_h) {
    for (int32_t dy = 0; dy < dst_h; dy++) {
        int32_t y0 = (int32_t)((int64_t)dy * src_h / dst_h);
        int32_t y1 = (int32_t)((int64_t)(dy + 1) * src_h / dst_h);
        if (y1 <= y0) y1 = y0 + 1;
        for (int32_t dx = 0; dx < dst_w; dx++) {
            int32_t x0 = (int32_t)((int64_t)dx * src_w / dst_w);
            int32_t x1 = (int32_t)((int64_t)(dx + 1) * src_w / dst_w);
            if (x1 <= x0) x1 = x0 + 1;
            uint32_t b = 0, g = 0, r = 0, n = 0;
            for (int32_t y = y0; y < y1; y++) {
                const uint8_t *p = src + (int64_t)y * src_stride + (int64_t)x0 * 4;
                for (int32_t x = x0; x < x1; x++, p += 4) {
                    b += p[0];
                    g += p[1];
                    r += p[2];
                    n++;
                }
            }
            uint8_t *o = dst + ((int64_t)dy * dst_w + dx) * 4;
            o[0] = (uint8_t)((b + n / 2) / n);
            o[1] = (uint8_t)((g + n / 2) / n);
            o[2] = (uint8_t)((r + n / 2) / n);
            o[3] = 255;
        }
    }
}
