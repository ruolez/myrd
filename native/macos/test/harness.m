// Exercises the macOS rc_native ABI without Go:
//   native/macos/test/run.sh
// Capture/input checks are skipped (not failed) when TCC permission is missing.
#import <Foundation/Foundation.h>
#import <CoreGraphics/CoreGraphics.h>
#include <stdatomic.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include "rc_native.h"

static atomic_int frames, keyframes, bad_annexb, key_without_ps, shapes, positions;
static atomic_long bytes;
static FILE *dump; // RC_HARNESS_DUMP=<file.h264> writes the elementary stream

static void on_frame(void *u, const uint8_t *d, int32_t len, int64_t pts, int32_t key) {
    atomic_fetch_add(&frames, 1);
    atomic_fetch_add(&bytes, len);
    if (dump) fwrite(d, 1, (size_t)len, dump);
    if (len < 5 || memcmp(d, "\0\0\0\1", 4) != 0) { atomic_fetch_add(&bad_annexb, 1); return; }
    int has_sps = 0, has_pps = 0, has_idr = 0;
    for (int32_t i = 0; i + 4 < len; i++) {
        if (d[i] == 0 && d[i + 1] == 0 && d[i + 2] == 0 && d[i + 3] == 1) {
            int t = d[i + 4] & 0x1f;
            if (t == 7) has_sps = 1;
            if (t == 8) has_pps = 1;
            if (t == 5) has_idr = 1;
        }
    }
    if (key) {
        atomic_fetch_add(&keyframes, 1);
        if (!(has_sps && has_pps && has_idr)) atomic_fetch_add(&key_without_ps, 1);
    }
}
static void on_shape(void *u, uint32_t id, uint16_t w, uint16_t h, uint16_t hx, uint16_t hy, const uint8_t *p, int32_t n) {
    atomic_fetch_add(&shapes, 1);
    if (n != w * h * 4) fprintf(stderr, "FAIL cursor shape len %d != %d\n", n, w * h * 4);
}
static void on_pos(void *u, uint16_t x, uint16_t y, uint8_t v) { atomic_fetch_add(&positions, 1); }

static int failures;
#define CHECK(cond, ...) do { if (cond) printf("ok   " __VA_ARGS__); else { printf("FAIL " __VA_ARGS__); failures++; } printf("\n"); } while (0)

int main(void) {
    @autoreleasepool {
        const char *dp = getenv("RC_HARNESS_DUMP");
        if (dp) dump = fopen(dp, "wb");
        CHECK(rc_init() == RC_OK, "rc_init");
        CHECK(strcmp(rc_backend_name(), "macos") == 0, "backend name %s", rc_backend_name());

        rc_monitor mons[8];
        int n = rc_list_monitors(mons, 8);
        CHECK(n > 0, "rc_list_monitors -> %d", n);
        int primaries = 0;
        for (int i = 0; i < n; i++) {
            printf("     monitor id=%u %ux%u @%d,%d scale=%.1f primary=%d name=\"%s\"\n", mons[i].id, mons[i].width,
                   mons[i].height, mons[i].x, mons[i].y, mons[i].scale, mons[i].primary, mons[i].name);
            primaries += mons[i].primary;
        }
        CHECK(primaries == 1, "exactly one primary monitor");

        int screen = -1, input = -1;
        CHECK(rc_check_permissions(&screen, &input) == RC_OK, "permissions screen=%d input=%d", screen, input);
        const char *enc = rc_encoder_name();
        CHECK(strncmp(enc, "videotoolbox", 12) == 0, "encoder probe: %s", enc);

        // Consent from a background thread with no AppKit run loop must not hang.
        __block int consent = 99;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        dispatch_async(dispatch_get_global_queue(QOS_CLASS_DEFAULT, 0), ^{
            consent = rc_consent_prompt("t", "m", 5);
            dispatch_semaphore_signal(sem);
        });
        long waited = dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
        CHECK(waited == 0 && consent == RC_ERR_UNSUPPORTED, "consent without run loop -> %d (no hang)", consent);

        char *clip = NULL;
        int32_t clen = 0;
        CHECK(rc_clipboard_get_text(&clip, &clen) == RC_OK, "clipboard read (%d bytes)", clen);
        rc_free(clip);

        rc_input_event mv = {0};
        mv.type = RC_EV_MOUSE_MOVE;
        mv.monitor_id = mons[0].id;
        int irc = rc_input_inject(&mv);
        if (input) {
            // Put the pointer back where it was: inject a move to the current location.
            CGEventRef e = CGEventCreate(NULL);
            CGPoint p = CGEventGetLocation(e);
            CFRelease(e);
            CGRect b = CGDisplayBounds(mons[0].id);
            mv.x = (uint16_t)((p.x - b.origin.x) / b.size.width * 65535);
            mv.y = (uint16_t)((p.y - b.origin.y) / b.size.height * 65535);
            CHECK(rc_input_inject(&mv) == RC_OK, "inject mouse move");
        } else {
            CHECK(irc == RC_ERR_PERMISSION, "inject without Accessibility -> RC_ERR_PERMISSION (%d)", irc);
        }

        rc_capture_config cfg = {.monitor_id = mons[0].id, .fps = 60, .bitrate_kbps = 6000,
                                 .keyframe_interval_ms = 2000, .max_width = 1920, .max_height = 1080};
        int crc = rc_capture_start(&cfg, on_frame, on_shape, on_pos, NULL);
        if (!screen) {
            CHECK(crc == RC_ERR_PERMISSION, "capture without Screen Recording -> RC_ERR_PERMISSION (%d)", crc);
        } else {
            CHECK(crc == RC_OK, "rc_capture_start -> %d (encoder %s)", crc, rc_encoder_name());
            CHECK(rc_capture_start(&cfg, on_frame, on_shape, on_pos, NULL) == RC_ERR_BUSY, "second start -> busy");
            usleep(1000 * 1000);
            int before = atomic_load(&keyframes);
            rc_request_keyframe();
            usleep(200 * 1000);
            CHECK(atomic_load(&keyframes) > before, "keyframe on request (static screen re-encode)");
            rc_encode_configure(2000, 30);
            usleep(800 * 1000);
            rc_capture_stop();
            int f = atomic_load(&frames);
            usleep(200 * 1000);
            CHECK(atomic_load(&frames) == f, "no frame callbacks after rc_capture_stop");
            CHECK(f >= 1, "frames=%d keyframes=%d bytes=%ld", f, atomic_load(&keyframes), atomic_load(&bytes));
            CHECK(atomic_load(&bad_annexb) == 0, "every AU starts with an Annex-B start code");
            CHECK(atomic_load(&key_without_ps) == 0, "every keyframe carries SPS+PPS+IDR");
            CHECK(atomic_load(&positions) >= 1, "cursor positions=%d shapes=%d", atomic_load(&positions),
                  atomic_load(&shapes));
        }
        rc_shutdown();
        if (dump) fclose(dump);
        printf("%s (%d failures)\n", failures ? "FAILED" : "PASSED", failures);
        return failures ? 1 : 0;
    }
}
