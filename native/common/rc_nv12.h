/*
 * rc_nv12.h — BGRA → NV12 (BT.709, limited range) on the CPU, for backends
 * without a GPU video processor (Windows 7: GDI capture into the Media
 * Foundation software encoder). Same matrix as rc_bgra_to_i444 in rc_vpx.c;
 * chroma is the average of each 2×2 block.
 */
#ifndef RC_NV12_H
#define RC_NV12_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* width and height must be even. y gets width×height luma bytes per row
 * stride ys; uv gets (width/2)×(height/2) interleaved Cb,Cr pairs per row
 * stride uvs. */
void rc_bgra_to_nv12(const uint8_t *bgra, int stride, int width, int height, uint8_t *y, int ys, uint8_t *uv,
                     int uvs);

#ifdef __cplusplus
}
#endif

#endif
