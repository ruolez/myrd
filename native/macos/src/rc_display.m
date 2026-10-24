// Lifecycle, monitors and TCC permissions.
#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#include <mach/mach_time.h>
#include <stdatomic.h>
#include <string.h>
#include "rc_internal.h"

int rc_init(void) {
    // Touch NSApplication so AppKit is initialised for windows/alerts. Does
    // not start a run loop; the tray (systray) owns that on the main thread.
    @autoreleasepool {
        if (NSApp == nil && [NSThread isMainThread]) {
            [NSApplication sharedApplication];
        }
    }
    return RC_OK;
}

void rc_shutdown(void) {
    rc_capture_stop();
    rc_wallpaper_hide(0);
    rc_indicator_show(0);
    rc_input_reset();
}

const char *rc_backend_name(void) { return "macos"; }

int64_t rc_now_us(void) {
    static mach_timebase_info_data_t tb;
    if (tb.denom == 0) mach_timebase_info(&tb);
    uint64_t t = mach_absolute_time();
    return (int64_t)((t * tb.numer / tb.denom) / 1000);
}

CGRect rc_display_bounds(uint32_t display_id) {
    CGDirectDisplayID ids[32];
    uint32_t n = 0;
    if (CGGetActiveDisplayList(32, ids, &n) != kCGErrorSuccess) return CGRectNull;
    for (uint32_t i = 0; i < n; i++) {
        if (ids[i] == display_id) return CGDisplayBounds(display_id);
    }
    return CGRectNull;
}

void rc_display_pixels(uint32_t display_id, uint32_t *w, uint32_t *h) {
    CGDisplayModeRef mode = CGDisplayCopyDisplayMode(display_id);
    if (mode) {
        *w = (uint32_t)CGDisplayModeGetPixelWidth(mode);
        *h = (uint32_t)CGDisplayModeGetPixelHeight(mode);
        CGDisplayModeRelease(mode);
    } else {
        *w = (uint32_t)CGDisplayPixelsWide(display_id);
        *h = (uint32_t)CGDisplayPixelsHigh(display_id);
    }
}

static void display_name(uint32_t display_id, char *out, size_t cap) {
    @autoreleasepool {
        NSString *name = nil;
        for (NSScreen *s in [NSScreen screens]) {
            NSNumber *num = s.deviceDescription[@"NSScreenNumber"];
            if (num && num.unsignedIntValue == display_id) {
                if (@available(macOS 10.15, *)) name = s.localizedName;
                break;
            }
        }
        if (name.length == 0) {
            name = CGDisplayIsBuiltin(display_id) ? @"Built-in Display"
                                                  : [NSString stringWithFormat:@"Display %u", display_id];
        }
        strlcpy(out, name.UTF8String ?: "Display", cap);
    }
}

int rc_list_monitors(rc_monitor *out, int max) {
    if (!out || max <= 0) return 0;
    CGDirectDisplayID ids[32];
    uint32_t n = 0;
    if (CGGetActiveDisplayList(32, ids, &n) != kCGErrorSuccess) return RC_ERR;
    int count = 0;
    for (uint32_t i = 0; i < n && count < max; i++) {
        CGDirectDisplayID d = ids[i];
        if (CGDisplayIsInMirrorSet(d) && CGDisplayMirrorsDisplay(d) != kCGNullDirectDisplay) continue;
        rc_monitor *m = &out[count++];
        memset(m, 0, sizeof(*m));
        CGRect b = CGDisplayBounds(d);
        uint32_t pw = 0, ph = 0;
        rc_display_pixels(d, &pw, &ph);
        m->id = d;
        m->x = (int32_t)b.origin.x;
        m->y = (int32_t)b.origin.y;
        m->width = pw;
        m->height = ph;
        m->scale = b.size.width > 0 ? (float)(pw / b.size.width) : 1.0f;
        m->primary = CGDisplayIsMain(d) ? 1 : 0;
        display_name(d, m->name, sizeof(m->name));
    }
    return count;
}

int rc_check_permissions(int *screen, int *input) {
    if (screen) *screen = CGPreflightScreenCaptureAccess() ? 1 : 0;
    if (input) *input = AXIsProcessTrusted() ? 1 : 0;
    return RC_OK;
}

static void open_pane(NSString *anchor) {
    NSString *url = [@"x-apple.systempreferences:com.apple.preference.security?" stringByAppendingString:anchor];
    [[NSWorkspace sharedWorkspace] openURL:[NSURL URLWithString:url]];
}

void rc_request_permission(int which) {
    @autoreleasepool {
        if (which == 1) {
            if (!CGPreflightScreenCaptureAccess()) CGRequestScreenCaptureAccess();
            open_pane(@"Privacy_ScreenCapture");
        } else if (which == 2) {
            NSDictionary *opts = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt: @YES};
            AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)opts);
            open_pane(@"Privacy_Accessibility");
        }
    }
}

// The main thread services the run loop if a block dispatched to the main
// queue runs within 300 ms.
BOOL rc_main_loop_alive(void) {
    if ([NSThread isMainThread]) return YES;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    dispatch_async(dispatch_get_main_queue(), ^{ dispatch_semaphore_signal(sem); });
    return dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 300 * NSEC_PER_MSEC)) == 0;
}

void rc_free(void *p) { free(p); }

// ---- display modes ----

// Every mode, the low-resolution (non-HiDPI) duplicates included.
static CFArrayRef copy_all_modes(CGDirectDisplayID d) {
    NSDictionary *opts = @{(__bridge NSString *)kCGDisplayShowDuplicateLowResolutionModes : @YES};
    return CGDisplayCopyAllDisplayModes(d, (__bridge CFDictionaryRef)opts);
}

static float mode_scale(CGDisplayModeRef m) {
    size_t w = CGDisplayModeGetWidth(m);
    return w ? (float)CGDisplayModeGetPixelWidth(m) / (float)w : 1.0f;
}

// Of two modes of one size, the better: HiDPI, then the refresh rate closest
// to the current one.
static bool better_mode(CGDisplayModeRef a, CGDisplayModeRef b, double hz) {
    float sa = mode_scale(a), sb = mode_scale(b);
    if (sa != sb) return sa > sb;
    return fabs(CGDisplayModeGetRefreshRate(a) - hz) < fabs(CGDisplayModeGetRefreshRate(b) - hz);
}

// The best desktop mode of display d at w×h points; NULL when there is none.
// The caller releases it.
static CGDisplayModeRef find_mode(CGDirectDisplayID d, uint32_t w, uint32_t h, double hz) {
    CFArrayRef all = copy_all_modes(d);
    if (!all) return NULL;
    CGDisplayModeRef best = NULL;
    for (CFIndex i = 0; i < CFArrayGetCount(all); i++) {
        CGDisplayModeRef m = (CGDisplayModeRef)CFArrayGetValueAtIndex(all, i);
        if (!CGDisplayModeIsUsableForDesktopGUI(m) || CGDisplayModeGetWidth(m) != w || CGDisplayModeGetHeight(m) != h)
            continue;
        if (!best || better_mode(m, best, hz)) best = m;
    }
    if (best) CGDisplayModeRetain(best);
    CFRelease(all);
    return best;
}

static int mode_cmp(const void *a, const void *b) {
    const rc_display_mode *x = a, *y = b;
    uint64_t ax = (uint64_t)x->width * x->height, ay = (uint64_t)y->width * y->height;
    if (ax != ay) return ax < ay ? 1 : -1;
    return x->width < y->width ? 1 : x->width > y->width ? -1 : 0;
}

int rc_display_modes(uint32_t monitor_id, rc_display_mode *out, int max, rc_display_mode *current) {
    if (!out || max <= 0) return RC_ERR;
    CGDirectDisplayID d = monitor_id ? monitor_id : CGMainDisplayID();
    CGDisplayModeRef cur = CGDisplayCopyDisplayMode(d);
    if (!cur) return RC_ERR;
    if (current)
        *current = (rc_display_mode){(uint32_t)CGDisplayModeGetWidth(cur), (uint32_t)CGDisplayModeGetHeight(cur),
                                     mode_scale(cur)};
    double hz = CGDisplayModeGetRefreshRate(cur);
    CGDisplayModeRelease(cur);
    CFArrayRef all = copy_all_modes(d);
    if (!all) return RC_ERR_UNSUPPORTED;
    int n = 0;
    for (CFIndex i = 0; i < CFArrayGetCount(all) && n < max; i++) {
        CGDisplayModeRef m = (CGDisplayModeRef)CFArrayGetValueAtIndex(all, i);
        if (!CGDisplayModeIsUsableForDesktopGUI(m)) continue;
        uint32_t w = (uint32_t)CGDisplayModeGetWidth(m), h = (uint32_t)CGDisplayModeGetHeight(m);
        bool dup = false;
        for (int j = 0; j < n && !dup; j++) dup = out[j].width == w && out[j].height == h;
        if (dup) continue;
        CGDisplayModeRef best = find_mode(d, w, h, hz);
        out[n++] = (rc_display_mode){w, h, best ? mode_scale(best) : mode_scale(m)};
        if (best) CGDisplayModeRelease(best);
    }
    CFRelease(all);
    qsort(out, (size_t)n, sizeof *out, mode_cmp);
    return n;
}

int rc_display_set_mode(uint32_t monitor_id, uint32_t width, uint32_t height) {
    CGDirectDisplayID d = monitor_id ? monitor_id : CGMainDisplayID();
    CGDisplayModeRef cur = CGDisplayCopyDisplayMode(d);
    double hz = cur ? CGDisplayModeGetRefreshRate(cur) : 0;
    if (cur) CGDisplayModeRelease(cur);
    CGDisplayModeRef mode = find_mode(d, width, height, hz);
    if (!mode) return RC_ERR_UNSUPPORTED;
    CGDisplayConfigRef cfg;
    CGError err = CGBeginDisplayConfiguration(&cfg);
    if (err == kCGErrorSuccess) {
        CGConfigureDisplayWithDisplayMode(cfg, d, mode, NULL);
        // For this login only: logout or rc_display_restore brings the saved mode back.
        err = CGCompleteDisplayConfiguration(cfg, kCGConfigureForSession);
    }
    CGDisplayModeRelease(mode);
    if (err != kCGErrorSuccess) {
        RCLOG("display %u: switch to %ux%u failed (%d)", d, width, height, err);
        return RC_ERR;
    }
    for (int i = 0; i < 20; i++) {
        CGRect b = CGDisplayBounds(d);
        if ((uint32_t)b.size.width == width && (uint32_t)b.size.height == height) break;
        usleep(100 * 1000);
    }
    return RC_OK;
}

int rc_display_restore(void) {
    CGRestorePermanentDisplayConfiguration();
    return RC_OK;
}
