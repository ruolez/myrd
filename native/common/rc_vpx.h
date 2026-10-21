/*
 * rc_vpx.h — the full-color encoder shared by every backend: VP9 profile 1
 * (4:4:4, 8-bit, BT.709 limited range) from libvpx, real-time, screen tuned.
 *
 * The backend captures and scales; this converts BGRA to 4:4:4 and encodes.
 * One encoder per capture; calls on one encoder must not run concurrently.
 */
#ifndef RC_VPX_H
#define RC_VPX_H

#include <stdint.h>

#include "rc_native.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct rc_vpx rc_vpx;

/* NULL when libvpx refuses the configuration. */
rc_vpx *rc_vpx_open(int width, int height, int fps, int kbps);
void rc_vpx_close(rc_vpx *e);
void rc_vpx_rate(rc_vpx *e, int kbps, int fps);

/* Converts a BGRA/BGRX picture of the encoder's size into its input. */
void rc_vpx_from_bgra(rc_vpx *e, const uint8_t *bgra, int stride);
/* The input planes (Y, U, V at full size) for callers that convert themselves. */
void rc_vpx_planes(rc_vpx *e, uint8_t *planes[3], int strides[3]);

/* Encodes the current input and passes the frame to emit (flags gain
 * RC_FRAME_KEY on a keyframe). key forces one; max_qp > 0 (H.264 scale,
 * 1..51) bounds the quantizer of this frame only: a refinement pass.
 * Returns RC_OK, or RC_ERR when libvpx failed. */
int rc_vpx_encode(rc_vpx *e, int64_t pts_us, int key, int max_qp, int32_t flags, rc_frame_fn emit, void *user);

/* BT.709 limited-range BGRA → 4:4:4, the conversion rc_vpx_from_bgra uses. */
void rc_bgra_to_i444(const uint8_t *bgra, int stride, int width, int height, uint8_t *y, int ys, uint8_t *u, int us,
                     uint8_t *v, int vs);

#ifdef __cplusplus
}
#endif

#endif /* RC_VPX_H */
