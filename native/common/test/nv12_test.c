/* BGRA → NV12 conversion: native/common/test/run.sh builds and runs it. */
#include "rc_nv12.h"

#include <stdio.h>
#include <string.h>

static int failures;

#define CHECK(cond, ...)                                                                                            \
    do {                                                                                                            \
        if (!(cond)) {                                                                                              \
            failures++;                                                                                             \
            printf("FAIL %s:%d: ", __FILE__, __LINE__);                                                             \
            printf(__VA_ARGS__);                                                                                    \
            printf("\n");                                                                                           \
        }                                                                                                           \
    } while (0)

typedef struct {
    uint8_t b, g, r;
    uint8_t y, cb, cr; /* BT.709 limited range, precomputed */
} color;

/* Y = 16 + 219·(.2126R + .7152G + .0722B)/255, Cb/Cr = 128 + 224·(…)/255. */
static const color colors[] = {
    {0, 0, 0, 16, 128, 128},       /* black */
    {255, 255, 255, 235, 128, 128}, /* white */
    {0, 0, 255, 63, 102, 240},     /* red */
    {0, 255, 0, 173, 42, 26},      /* green */
    {255, 0, 0, 32, 240, 118},     /* blue */
};
#define NCOLORS (int)(sizeof(colors) / sizeof(colors[0]))

static void fill(uint8_t *px, const color *c) {
    px[0] = c->b;
    px[1] = c->g;
    px[2] = c->r;
    px[3] = 0xff;
}

/* One flat 2×2 block per color, side by side, in rows padded past the image. */
static void solid_blocks(void) {
    enum { W = 2 * NCOLORS, H = 2, STRIDE = W * 4 + 12, YS = W + 3, UVS = W + 5 };
    uint8_t bgra[STRIDE * H], y[YS * H], uv[UVS * H / 2];
    memset(bgra, 0xaa, sizeof(bgra));
    memset(y, 0xee, sizeof(y));
    memset(uv, 0xee, sizeof(uv));
    for (int row = 0; row < H; row++)
        for (int x = 0; x < W; x++) fill(bgra + row * STRIDE + x * 4, &colors[x / 2]);

    rc_bgra_to_nv12(bgra, STRIDE, W, H, y, YS, uv, UVS);

    for (int i = 0; i < NCOLORS; i++) {
        const color *c = &colors[i];
        for (int row = 0; row < H; row++)
            for (int dx = 0; dx < 2; dx++) {
                int got = y[row * YS + 2 * i + dx];
                CHECK(got == c->y, "color %d luma at (%d,%d) = %d, want %d", i, 2 * i + dx, row, got, c->y);
            }
        CHECK(uv[2 * i] == c->cb && uv[2 * i + 1] == c->cr, "color %d chroma = (%d,%d), want (%d,%d)", i, uv[2 * i],
              uv[2 * i + 1], c->cb, c->cr);
    }
    CHECK(y[W] == 0xee && y[YS + W] == 0xee, "luma padding was written");
    CHECK(uv[W] == 0xee, "chroma padding was written");
}

/* A black/white checkerboard keeps full-resolution luma and averages chroma
 * to neutral grey within each 2×2 block. */
static void checkerboard(void) {
    enum { W = 4, H = 4 };
    uint8_t bgra[W * H * 4], y[W * H], uv[W * H / 2];
    for (int row = 0; row < H; row++)
        for (int x = 0; x < W; x++) fill(bgra + (row * W + x) * 4, &colors[(x + row) & 1]);

    rc_bgra_to_nv12(bgra, W * 4, W, H, y, W, uv, W);

    for (int row = 0; row < H; row++)
        for (int x = 0; x < W; x++) {
            int want = colors[(x + row) & 1].y;
            CHECK(y[row * W + x] == want, "checker luma at (%d,%d) = %d, want %d", x, row, y[row * W + x], want);
        }
    for (int i = 0; i < W * H / 2; i++) CHECK(uv[i] == 128, "checker chroma byte %d = %d, want 128", i, uv[i]);
}

/* Chroma is the mean of the block: one red pixel among three black ones
 * moves Cr a quarter of the way from black's to red's. */
static void chroma_is_the_block_mean(void) {
    uint8_t bgra[2 * 2 * 4], y[4], uv[2];
    for (int i = 0; i < 4; i++) fill(bgra + i * 4, &colors[i == 0 ? 2 : 0]);
    rc_bgra_to_nv12(bgra, 8, 2, 2, y, 2, uv, 2);
    /* mean pixel (63.75, 0, 0): Cb = 128 − 25.8·0.25 ≈ 121.6, Cr = 128 + 112·0.25 = 156 */
    CHECK(uv[0] == 122 && uv[1] == 156, "mixed block chroma = (%d,%d), want (122,156)", uv[0], uv[1]);
    CHECK(y[0] == 63 && y[1] == 16 && y[2] == 16 && y[3] == 16, "mixed block luma = %d %d %d %d", y[0], y[1], y[2],
          y[3]);
}

int main(void) {
    solid_blocks();
    checkerboard();
    chroma_is_the_block_mean();
    if (failures) {
        printf("nv12: %d failure(s)\n", failures);
        return 1;
    }
    printf("nv12: ok\n");
    return 0;
}
