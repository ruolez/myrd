#include "rc_nv12.h"

#include <stddef.h>

/* 16.16 fixed point, the matrix of rc_bgra_to_i444. */
static inline uint8_t luma(int r, int g, int b) {
    return (uint8_t)((11966 * r + 40254 * g + 4064 * b + (16 << 16) + 32768) >> 16);
}

void rc_bgra_to_nv12(const uint8_t *bgra, int stride, int width, int height, uint8_t *y, int ys, uint8_t *uv,
                     int uvs) {
    for (int row = 0; row < height; row += 2) {
        const uint8_t *p0 = bgra + (size_t)row * stride, *p1 = p0 + stride;
        uint8_t *y0 = y + (size_t)row * ys, *y1 = y0 + ys;
        uint8_t *c = uv + (size_t)(row / 2) * uvs;
        for (int x = 0; x < width; x += 2, p0 += 8, p1 += 8, c += 2) {
            y0[x] = luma(p0[2], p0[1], p0[0]);
            y0[x + 1] = luma(p0[6], p0[5], p0[4]);
            y1[x] = luma(p1[2], p1[1], p1[0]);
            y1[x + 1] = luma(p1[6], p1[5], p1[4]);
            int b = (p0[0] + p0[4] + p1[0] + p1[4] + 2) >> 2;
            int g = (p0[1] + p0[5] + p1[1] + p1[5] + 2) >> 2;
            int r = (p0[2] + p0[6] + p1[2] + p1[6] + 2) >> 2;
            c[0] = (uint8_t)((-6596 * r - 22189 * g + 28785 * b + (128 << 16) + 32768) >> 16);
            c[1] = (uint8_t)((28785 * r - 26145 * g - 2640 * b + (128 << 16) + 32768) >> 16);
        }
    }
}
