// Clipboard, the always-on-top session border and the on-device consent prompt.
#import <AppKit/AppKit.h>
#include <string.h>
#include "rc_internal.h"

// ---------------- clipboard ----------------

int rc_clipboard_get_text(char **out, int32_t *len) {
    if (!out || !len) return RC_ERR;
    @autoreleasepool {
        NSString *s = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
        if (!s) { *out = NULL; *len = 0; return RC_OK; }
        const char *u = s.UTF8String;
        size_t n = strlen(u);
        char *buf = malloc(n + 1);
        if (!buf) return RC_ERR;
        memcpy(buf, u, n + 1);
        *out = buf;
        *len = (int32_t)n;
    }
    return RC_OK;
}

int rc_clipboard_set_text(const char *utf8, int32_t len) {
    if (!utf8 || len < 0) return RC_ERR;
    @autoreleasepool {
        NSString *s = [[NSString alloc] initWithBytes:utf8 length:(NSUInteger)len encoding:NSUTF8StringEncoding];
        if (!s) return RC_ERR;
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        return [pb setString:s forType:NSPasteboardTypeString] ? RC_OK : RC_ERR;
    }
}

// ---------------- session indicator ----------------

@interface RCBorderView : NSView
@end
@implementation RCBorderView
- (void)drawRect:(NSRect)dirty {
    [[NSColor colorWithSRGBRed:0.85 green:0.19 blue:0.15 alpha:0.95] setStroke];
    NSBezierPath *p = [NSBezierPath bezierPathWithRect:NSInsetRect(self.bounds, 3, 3)];
    p.lineWidth = 6;
    [p stroke];
}
@end

static NSMutableArray<NSWindow *> *g_borders;
static BOOL g_indicator_on;

static void show_borders(void) {
    for (NSWindow *w in g_borders) [w orderOut:nil];
    g_borders = [NSMutableArray array];
    for (NSScreen *screen in [NSScreen screens]) {
        NSWindow *w = [[NSWindow alloc] initWithContentRect:screen.frame
                                                  styleMask:NSWindowStyleMaskBorderless
                                                    backing:NSBackingStoreBuffered
                                                      defer:NO];
        w.releasedWhenClosed = NO;
        w.opaque = NO;
        w.backgroundColor = [NSColor clearColor];
        w.hasShadow = NO;
        w.ignoresMouseEvents = YES;
        w.level = NSScreenSaverWindowLevel;
        w.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorStationary |
                               NSWindowCollectionBehaviorFullScreenAuxiliary | NSWindowCollectionBehaviorIgnoresCycle;
        w.sharingType = NSWindowSharingNone; // visible on the device, absent from the capture
        w.contentView = [[RCBorderView alloc] initWithFrame:NSMakeRect(0, 0, screen.frame.size.width,
                                                                        screen.frame.size.height)];
        [w setFrame:screen.frame display:YES];
        [w orderFrontRegardless];
        [g_borders addObject:w];
    }
}

static void hide_borders(void) {
    for (NSWindow *w in g_borders) [w orderOut:nil];
    g_borders = nil;
}

static void screens_changed(void) {
    if (g_indicator_on) show_borders();
}

void rc_indicator_show(int on) {
    dispatch_async(dispatch_get_main_queue(), ^{
        static id observer;
        if (!observer) {
            observer = [[NSNotificationCenter defaultCenter]
                addObserverForName:NSApplicationDidChangeScreenParametersNotification
                            object:nil
                             queue:[NSOperationQueue mainQueue]
                        usingBlock:^(NSNotification *n) { screens_changed(); }];
        }
        g_indicator_on = on != 0;
        if (on) show_borders();
        else hide_borders();
    });
}

// ---------------- consent prompt ----------------

static int run_alert(NSString *title, NSString *message, int32_t timeout_s) {
    NSAlert *a = [[NSAlert alloc] init];
    a.alertStyle = NSAlertStyleWarning;
    a.messageText = title;
    a.informativeText = [NSString stringWithFormat:@"%@\n\nThis prompt closes in %d seconds and denies by default.",
                                                   message, timeout_s];
    [a addButtonWithTitle:@"Deny"];               // NSAlertFirstButtonReturn, default (Return)
    [a addButtonWithTitle:@"Allow once"];         // NSAlertSecondButtonReturn
    [a addButtonWithTitle:@"Allow and remember"]; // NSAlertThirdButtonReturn
    a.buttons[0].keyEquivalent = @"\r";
    a.window.level = NSModalPanelWindowLevel + 1;
    a.window.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces;

    [NSApp activateIgnoringOtherApps:YES];
    NSTimer *t = [NSTimer timerWithTimeInterval:timeout_s repeats:NO block:^(NSTimer *timer) {
        [NSApp stopModalWithCode:NSModalResponseAbort];
    }];
    [[NSRunLoop currentRunLoop] addTimer:t forMode:NSModalPanelRunLoopMode];
    NSModalResponse r = [a runModal];
    [t invalidate];
    [a.window orderOut:nil];
    if (r == NSAlertSecondButtonReturn) return 1;
    if (r == NSAlertThirdButtonReturn) return 2;
    return 0;
}

int rc_consent_prompt(const char *title, const char *message, int32_t timeout_s) {
    if (timeout_s <= 0) timeout_s = 60;
    NSString *t = title ? [NSString stringWithUTF8String:title] : @"Remote session request";
    NSString *m = message ? [NSString stringWithUTF8String:message] : @"";
    if ([NSThread isMainThread]) return run_alert(t, m, timeout_s);
    // No AppKit run loop on the main thread (e.g. `selftest`): never block forever.
    if (NSApp == nil || !rc_main_loop_alive()) return RC_ERR_UNSUPPORTED;

    __block int result = 0;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    dispatch_async(dispatch_get_main_queue(), ^{
        result = run_alert(t, m, timeout_s);
        dispatch_semaphore_signal(sem);
    });
    if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(timeout_s + 10) * NSEC_PER_SEC)) != 0)
        return 0;
    return result;
}
