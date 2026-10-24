/* rc_scale.h — BGRA downscaling for rc_snapshot (box filter) and frame probes. */
#ifndef RC_SCALE_H
#define RC_SCALE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* The size that fits w×h into max_w×max_h keeping the aspect ratio, never
 * larger than w×h, at least 1×1. */
void rc_fit(int32_t w, int32_t h, int32_t max_w, int32_t max_h, int32_t *out_w, int32_t *out_h);

/* Area-averages a BGRA image (src_stride bytes per row) into dst
 * (dst_w×dst_h, tightly packed). dst_w <= src_w and dst_h <= src_h.
 * The alpha channel is set opaque. */
void rc_scale_bgra(const uint8_t *src, int32_t src_w, int32_t src_h, int32_t src_stride,
                   uint8_t *dst, int32_t dst_w, int32_t dst_h);

/* The fraction (0..1) of black pixels (every channel below 8) on a grid of
 * up to 32×32 samples of a BGRA image. */
float rc_dark_fraction(const uint8_t *px, int32_t w, int32_t h, int32_t stride);

#ifdef __cplusplus
}
#endif

#endif
