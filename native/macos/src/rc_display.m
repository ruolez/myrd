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
