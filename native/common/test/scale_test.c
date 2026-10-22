/* BGRA downscaling for previews: native/common/test/run.sh builds and runs it. */
#include "rc_scale.h"

#include <stdio.h>
#include <stdlib.h>

static int failures;

#define CHECK(cond, ...)                                       \
    do {                                                       \
        if (!(cond)) {                                         \
            failures++;                                        \
            printf("FAIL %s:%d: ", __FILE__, __LINE__);        \
            printf(__VA_ARGS__);                               \
            printf("\n");                                      \
        }                                                      \
    } while (0)

static void test_fit(void) {
    static const int32_t cases[][6] = {
        /* w, h, max_w, max_h, want_w, want_h */
        {3840, 2160, 480, 480, 480, 270},
        {1080, 1920, 480, 480, 270, 480},
        {320, 200, 480, 480, 320, 200}, /* never enlarged */
        {5120, 1440, 480, 270, 480, 135},
        {1, 10000, 480, 480, 1, 480},  /* at least one pixel wide */
        {0, 0, 480, 480, 1, 1},
    };
    for (size_t i = 0; i < sizeof cases / sizeof cases[0]; i++) {
        int32_t w, h;
        rc_fit(cases[i][0], cases[i][1], cases[i][2], cases[i][3], &w, &h);
        CHECK(w == cases[i][4] && h == cases[i][5], "fit %d×%d in %d×%d = %d×%d, want %d×%d", cases[i][0],
              cases[i][1], cases[i][2], cases[i][3], w, h, cases[i][4], cases[i][5]);
    }
}

/* A 4×2 image of two flat 2×2 halves (black, white) with padded rows halves
 * to 2×1: one black and one white pixel; a checkerboard averages to grey. */
static void test_scale(void) {
    const int32_t stride = 4 * 4 + 8; /* padded rows */
    uint8_t src[2 * (4 * 4 + 8)] = {0};
    for (int y = 0; y < 2; y++)
        for (int x = 0; x < 4; x++) {
            uint8_t v = x < 2 ? 0 : 255;
            uint8_t *p = src + y * stride + x * 4;
            p[0] = p[1] = p[2] = v;
            p[3] = 0; /* alpha is ignored */
        }
    uint8_t dst[2 * 4];
    rc_scale_bgra(src, 4, 2, stride, dst, 2, 1);
    CHECK(dst[0] == 0 && dst[3] == 255, "left half black, opaque: %d a=%d", dst[0], dst[3]);
    CHECK(dst[4] == 255 && dst[5] == 255 && dst[6] == 255, "right half white: %d", dst[4]);

    uint8_t checker[4 * 4 * 4];
    for (int i = 0; i < 16; i++) {
        uint8_t v = ((i % 4) + (i / 4)) % 2 ? 255 : 0;
        checker[i * 4] = checker[i * 4 + 1] = checker[i * 4 + 2] = v;
    }
    uint8_t one[4];
    rc_scale_bgra(checker, 4, 4, 16, one, 1, 1);
    CHECK(one[0] == 128 && one[1] == 128 && one[2] == 128, "checkerboard averages to %d", one[0]);

    /* Odd ratios cover every source column exactly once: spans [0,2) [2,4) [4,7). */
    uint8_t ramp[7 * 4];
    for (int x = 0; x < 7; x++) ramp[x * 4] = ramp[x * 4 + 1] = ramp[x * 4 + 2] = (uint8_t)(x * 10);
    uint8_t three[3 * 4];
    rc_scale_bgra(ramp, 7, 1, 28, three, 3, 1);
    CHECK(three[0] == 5 && three[4] == 25 && three[8] == 50, "ramp 7→3: %d %d %d", three[0], three[4], three[8]);
}

int main(void) {
    test_fit();
    test_scale();
    if (failures) {
        printf("%d failure(s)\n", failures);
        return 1;
    }
    printf("rc_scale: ok\n");
    return 0;
}
