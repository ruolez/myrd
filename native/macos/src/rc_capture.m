// Capture (ScreenCaptureKit) → hardware H.264 (VideoToolbox) → Annex-B
// access units, or BGRA → VP9 4:4:4 (rc_vpx, full color), plus the
// out-of-band cursor stream.
#import <AppKit/AppKit.h>
#import <IOKit/pwr_mgt/IOPMLib.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <VideoToolbox/VideoToolbox.h>
#import <CoreMedia/CoreMedia.h>
#include <pthread.h>
#include <stdatomic.h>
#include <string.h>
#include <unistd.h>
#include "rc_internal.h"
#include "rc_scale.h"
#include "rc_vpx.h"

@interface RCStreamOutput : NSObject <SCStreamOutput, SCStreamDelegate>
@end

static pthread_mutex_t g_ctl = PTHREAD_MUTEX_INITIALIZER; // start/stop/configure
static pthread_mutex_t g_cb = PTHREAD_MUTEX_INITIALIZER;  // frame callback
static atomic_bool g_running;
static atomic_bool g_force_key;

static rc_frame_fn g_on_frame;
static rc_cursor_shape_fn g_on_shape;
static rc_cursor_pos_fn g_on_pos;
static void *g_user;

static SCStream *g_stream;
static SCStreamConfiguration *g_sc;
static RCStreamOutput *g_output;
static dispatch_queue_t g_queue;        // sample handler + encode submit (serial)
static dispatch_queue_t g_cursor_queue; // cursor polling (serial)
static dispatch_source_t g_cursor_timer;
static dispatch_queue_t g_wp_queue;     // wallpaper filter refresh (serial)
static dispatch_source_t g_wp_timer;
static atomic_bool g_hide_wp;
static NSSet<NSNumber *> *g_wp_ids;     // windows the current filter leaves out (hidden mode)
static VTCompressionSessionRef g_vt;
static CVPixelBufferRef g_last;
static int64_t g_last_pts;
static int64_t g_last_content_us; // g_queue only
static bool g_clamped;            // MaxAllowedFrameQP is set below 51 (g_queue only)
static atomic_bool g_refine_ok;   // the encoder accepts MaxAllowedFrameQP

// Full color: SCK delivers BGRA and rc_vpx encodes on g_vpx_queue. Encoding
// takes longer than a frame at large sizes, so only the newest picture waits.
static int g_codec; // RC_CODEC_* of the running capture
static rc_vpx *g_vpx;
static dispatch_queue_t g_vpx_queue;
static pthread_mutex_t g_vpx_mu = PTHREAD_MUTEX_INITIALIZER;
static CVPixelBufferRef g_vpx_pb; // newest picture waiting for the encoder
static int64_t g_vpx_pts;
static bool g_vpx_key, g_vpx_scheduled;
static int g_vpx_qp;
static int32_t g_vpx_flags;

static uint32_t g_display;
static CGRect g_bounds;
static int g_capw, g_caph, g_fps, g_kbps, g_gop_ms;
static uint32_t g_pw; // native pixel width of the captured display
static char g_encoder[32];

static uint8_t *g_out;
static size_t g_out_cap;

// Unlock screen: ScreenCaptureKit delivers it black, so while the session is
// locked the pictures come from CGDisplayStream (on g_queue) and SCK's are
// dropped. Checked once a second by the g_wp_timer tick.
static CGDisplayStreamRef g_lock_stream;
static atomic_bool g_lock_active;

// A capture wakes a sleeping display and keeps it on: the unlock screen turns
// it off after a minute without local input.
static IOPMAssertionID g_activity_id, g_awake_id;

// ---------------- encoder ----------------

static void out_reserve(size_t n) {
    if (n <= g_out_cap) return;
    size_t cap = g_out_cap ? g_out_cap : 256 * 1024;
    while (cap < n) cap *= 2;
    g_out = realloc(g_out, cap);
    g_out_cap = cap;
}

static const uint8_t kStart[4] = {0, 0, 0, 1};

static void vt_output(void *refcon, void *frame_refcon, OSStatus status, VTEncodeInfoFlags flags,
                      CMSampleBufferRef sb) {
    if (status != noErr || sb == NULL || (flags & kVTEncodeInfo_FrameDropped)) return;
    if (!atomic_load(&g_running) || !CMSampleBufferDataIsReady(sb)) return;

    bool key = true;
    CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
    if (atts && CFArrayGetCount(atts) > 0) {
        CFDictionaryRef d = CFArrayGetValueAtIndex(atts, 0);
        CFBooleanRef not_sync = NULL;
        if (CFDictionaryGetValueIfPresent(d, kCMSampleAttachmentKey_NotSync, (const void **)&not_sync) && not_sync)
            key = !CFBooleanGetValue(not_sync);
    }

    CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sb);
    size_t ps_count = 0;
    int nal_len = 4;
    CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, NULL, NULL, &ps_count, &nal_len);
    if (nal_len < 1 || nal_len > 4) return;

    CMBlockBufferRef bb = CMSampleBufferGetDataBuffer(sb);
    size_t total = CMBlockBufferGetDataLength(bb);
    if (total == 0) return;

    int64_t pts_us = (int64_t)(CMTimeGetSeconds(CMSampleBufferGetPresentationTimeStamp(sb)) * 1e6);

    pthread_mutex_lock(&g_cb);
    if (!atomic_load(&g_running) || g_on_frame == NULL) {
        pthread_mutex_unlock(&g_cb);
        return;
    }

    size_t ps_bytes = 0;
    if (key) {
        for (size_t i = 0; i < ps_count; i++) {
            const uint8_t *ps;
            size_t ps_size;
            if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, i, &ps, &ps_size, NULL, NULL) == noErr)
                ps_bytes += 4 + ps_size;
        }
    }
    // Worst case every NAL length prefix (>=1 byte) grows to a 4-byte start code.
    out_reserve(ps_bytes + total * 4 + 64);

    size_t n = 0;
    if (key) {
        for (size_t i = 0; i < ps_count; i++) {
            const uint8_t *ps;
            size_t ps_size;
            if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, i, &ps, &ps_size, NULL, NULL) != noErr)
                continue;
            memcpy(g_out + n, kStart, 4);
            memcpy(g_out + n + 4, ps, ps_size);
            n += 4 + ps_size;
        }
    }

    char *data = NULL;
    size_t contiguous = 0;
    uint8_t *tmp = NULL;
    if (CMBlockBufferGetDataPointer(bb, 0, &contiguous, NULL, &data) != kCMBlockBufferNoErr || contiguous < total) {
        tmp = malloc(total);
        if (CMBlockBufferCopyDataBytes(bb, 0, total, tmp) != kCMBlockBufferNoErr) {
            free(tmp);
            pthread_mutex_unlock(&g_cb);
            return;
        }
        data = (char *)tmp;
    }

    size_t off = 0;
    while (off + (size_t)nal_len <= total) {
        uint32_t len = 0;
        for (int i = 0; i < nal_len; i++) len = (len << 8) | (uint8_t)data[off + i];
        off += nal_len;
        if (len == 0 || off + len > total) break;
        memcpy(g_out + n, kStart, 4);
        memcpy(g_out + n + 4, data + off, len);
        n += 4 + len;
        off += len;
    }
    free(tmp);

    int fl = key ? RC_FRAME_KEY : (int)(intptr_t)frame_refcon & (RC_FRAME_STATIC | RC_FRAME_MINOR);
    if (n > ps_bytes) g_on_frame(g_user, g_out, (int32_t)n, pts_us, fl);
    pthread_mutex_unlock(&g_cb);
}

static void vt_set_rate(VTCompressionSessionRef s, int kbps, int fps) {
    int bps = kbps * 1000;
    VTSessionSetProperty(s, kVTCompressionPropertyKey_AverageBitRate, (__bridge CFNumberRef)@(bps));
    // Cap any 1 s window at 1.5x the target so a scene change cannot burst the link.
    NSArray *limits = @[ @((long long)bps / 8 * 3 / 2), @1.0 ];
    VTSessionSetProperty(s, kVTCompressionPropertyKey_DataRateLimits, (__bridge CFArrayRef)limits);
    VTSessionSetProperty(s, kVTCompressionPropertyKey_ExpectedFrameRate, (__bridge CFNumberRef)@(fps));
}

// Creates a session trying, in order: low-latency hardware rate control
// (constrained high when the browser negotiated high, else constrained
// baseline; the only two profiles it supports), plain hardware, software.
static VTCompressionSessionRef vt_create(int w, int h, int fps, int kbps, int gop_ms, int profile_idc, char *name,
                                         size_t name_cap, bool *refine_ok) {
    NSDictionary *ll = @{(__bridge NSString *)kVTVideoEncoderSpecification_EnableLowLatencyRateControl: @YES};
    NSDictionary *hw = @{(__bridge NSString *)kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder: @YES};
    BOOL high = profile_idc >= 100;
    CFStringRef fallback = high ? kVTProfileLevel_H264_High_AutoLevel : kVTProfileLevel_H264_Main_AutoLevel;
    struct { NSDictionary *spec; CFStringRef profile; } attempts[] = {
        {ll, high ? kVTProfileLevel_H264_ConstrainedHigh_AutoLevel : kVTProfileLevel_H264_ConstrainedBaseline_AutoLevel},
        {ll, kVTProfileLevel_H264_ConstrainedBaseline_AutoLevel},
        {hw, fallback},
        {nil, fallback},
    };
    for (size_t i = 0; i < sizeof(attempts) / sizeof(attempts[0]); i++) {
        if (i == 1 && !high) continue; // same as attempt 0
        VTCompressionSessionRef s = NULL;
        OSStatus st = VTCompressionSessionCreate(kCFAllocatorDefault, w, h, kCMVideoCodecType_H264,
                                                 (__bridge CFDictionaryRef)attempts[i].spec, NULL, NULL,
                                                 vt_output, NULL, &s);
        if (st != noErr || s == NULL) continue;
        // The low-latency encoder refuses profiles it lacks: try the next one.
        if (VTSessionSetProperty(s, kVTCompressionPropertyKey_ProfileLevel, attempts[i].profile) != noErr &&
            attempts[i].spec == ll) {
            VTCompressionSessionInvalidate(s);
            CFRelease(s);
            continue;
        }
        VTSessionSetProperty(s, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration,
                             (__bridge CFNumberRef)@(gop_ms / 1000.0));
        // Frame-count limit sized for the 60 fps ceiling (fps changes at runtime).
        VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxKeyFrameInterval,
                             (__bridge CFNumberRef)@(MAX(1, 60 * gop_ms / 1000)));
        VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxFrameDelayCount, (__bridge CFNumberRef)@0);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_ColorPrimaries, kCMFormatDescriptionColorPrimaries_ITU_R_709_2);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_TransferFunction, kCMFormatDescriptionTransferFunction_ITU_R_709_2);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_YCbCrMatrix, kCMFormatDescriptionYCbCrMatrix_ITU_R_709_2);
        vt_set_rate(s, kbps, fps);
        // Refinement passes bound the quantizer of one frame (rc_encode_refine).
        *refine_ok = VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxAllowedFrameQP,
                                          (__bridge CFNumberRef)@51) == noErr;
        VTCompressionSessionPrepareToEncodeFrames(s);

        // The low-latency encoder (rtvc) is hardware-only and does not answer this query.
        CFBooleanRef using_hw = NULL;
        OSStatus qs = VTSessionCopyProperty(s, kVTCompressionPropertyKey_UsingHardwareAcceleratedVideoEncoder,
                                            kCFAllocatorDefault, &using_hw);
        BOOL hardware = qs == noErr ? (using_hw && CFBooleanGetValue(using_hw)) : (attempts[i].spec == ll);
        if (using_hw) CFRelease(using_hw);
        strlcpy(name, hardware ? "videotoolbox" : "videotoolbox-sw", name_cap);
        RCLOG(@"encoder attempt %zu ok, hardware=%d profile=%@ refine=%d", i, hardware, attempts[i].profile,
              *refine_ok);
        return s;
    }
    return NULL;
}

static void vpx_emit(void *user, const uint8_t *data, int32_t len, int64_t pts_us, int32_t flags) {
    pthread_mutex_lock(&g_cb);
    if (atomic_load(&g_running) && g_on_frame) g_on_frame(g_user, data, len, pts_us, flags);
    pthread_mutex_unlock(&g_cb);
}

// Encodes whatever picture is waiting until none is; runs on g_vpx_queue.
static void vpx_drain(void) {
    for (;;) {
        pthread_mutex_lock(&g_vpx_mu);
        CVPixelBufferRef pb = g_vpx_pb;
        bool key = g_vpx_key;
        int qp = g_vpx_qp;
        int32_t flags = g_vpx_flags;
        int64_t pts = g_vpx_pts;
        g_vpx_pb = NULL;
        g_vpx_key = false;
        g_vpx_qp = 0;
        if (!pb) g_vpx_scheduled = false;
        pthread_mutex_unlock(&g_vpx_mu);
        if (!pb) return;
        if (g_vpx && atomic_load(&g_running) && CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly) == kCVReturnSuccess) {
            rc_vpx_from_bgra(g_vpx, CVPixelBufferGetBaseAddress(pb), (int)CVPixelBufferGetBytesPerRow(pb));
            CVPixelBufferUnlockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
            rc_vpx_encode(g_vpx, pts, key, qp, key ? 0 : flags, vpx_emit, NULL);
        }
        CVPixelBufferRelease(pb);
    }
}

// Queues pb for the VP9 encoder, replacing a picture still waiting; a
// refinement pass (flagged static) never replaces new content, and replaced
// content stays minor only if both changes were.
static void vpx_submit(CVPixelBufferRef pb, bool force_key, int max_qp, int32_t flags, int64_t pts) {
    pthread_mutex_lock(&g_vpx_mu);
    if ((flags & RC_FRAME_STATIC) && g_vpx_pb && !(g_vpx_flags & RC_FRAME_STATIC)) {
        pthread_mutex_unlock(&g_vpx_mu);
        return;
    }
    if (g_vpx_pb && g_vpx_qp == 0 && max_qp == 0) flags &= g_vpx_flags;
    CVPixelBufferRetain(pb);
    if (g_vpx_pb) CVPixelBufferRelease(g_vpx_pb);
    g_vpx_pb = pb;
    g_vpx_pts = pts;
    g_vpx_key = g_vpx_key || force_key;
    g_vpx_qp = max_qp;
    g_vpx_flags = flags;
    bool schedule = !g_vpx_scheduled;
    g_vpx_scheduled = true;
    pthread_mutex_unlock(&g_vpx_mu);
    if (schedule) dispatch_async(g_vpx_queue, ^{ vpx_drain(); });
}

// max_qp > 0 encodes a refinement pass: the frame's quantizer is bounded and
// it is flagged static; any other frame lifts the bound first. flags adds
// RC_FRAME_MINOR to new content.
static void encode_pixbuf(CVPixelBufferRef pb, bool force_key, int max_qp, int32_t flags) {
    if (!pb || (!g_vt && !g_vpx)) return;
    int64_t now = rc_now_us();
    if (now <= g_last_pts) now = g_last_pts + 1;
    g_last_pts = now;
    if (max_qp > 0) flags = RC_FRAME_STATIC;
    if (g_vpx) {
        vpx_submit(pb, force_key, max_qp, flags, now);
        return;
    }
    if (max_qp > 0 || g_clamped) {
        VTSessionSetProperty(g_vt, kVTCompressionPropertyKey_MaxAllowedFrameQP,
                             (__bridge CFNumberRef)@(max_qp > 0 ? max_qp : 51));
        g_clamped = max_qp > 0;
    }
    NSDictionary *opts = force_key ? @{(__bridge NSString *)kVTEncodeFrameOptionKey_ForceKeyFrame: @YES} : nil;
    VTCompressionSessionEncodeFrame(g_vt, pb, CMTimeMake(now, 1000000), kCMTimeInvalid,
                                    (__bridge CFDictionaryRef)opts, (void *)(intptr_t)flags, NULL);
}

// ---------------- stream output ----------------

// RC_FRAME_MINOR when the frame's dirty rects cover at most 2% of it.
static int32_t minor_change(NSArray *dirty, CVPixelBufferRef pb) {
    if (![dirty isKindOfClass:[NSArray class]] || dirty.count == 0) return 0;
    double area = 0, total = (double)CVPixelBufferGetWidth(pb) * CVPixelBufferGetHeight(pb);
    for (id d in dirty) {
        CGRect r;
        if ([d isKindOfClass:[NSDictionary class]] && CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)d, &r))
            area += r.size.width * r.size.height;
    }
    return total > 0 && area <= total * 0.02 ? RC_FRAME_MINOR : 0;
}

@implementation RCStreamOutput
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen || !atomic_load(&g_running) || atomic_load(&g_lock_active)) return;
    CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
    if (!atts || CFArrayGetCount(atts) == 0) return;
    NSDictionary *a = (__bridge NSDictionary *)CFArrayGetValueAtIndex(atts, 0);
    NSNumber *status = a[SCStreamFrameInfoStatus];
    // Idle / blank / suspended frames carry no new pixels: a static screen costs nothing.
    if (status == nil || status.integerValue != SCFrameStatusComplete) return;
    CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sb);
    if (!pb) return;
    CVPixelBufferRetain(pb);
    if (g_last) CVPixelBufferRelease(g_last);
    g_last = pb;
    g_last_content_us = rc_now_us();
    encode_pixbuf(pb, atomic_exchange(&g_force_key, false), 0, minor_change(a[SCStreamFrameInfoDirtyRects], pb));
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    RCLOG(@"stream stopped: %@", error);
}
@end

// ---------------- cursor ----------------

static uint32_t g_shape_id;
static uint16_t g_pos_x = 0xFFFF, g_pos_y = 0xFFFF;
static int g_pos_vis = -1;
static unsigned g_tick;

static uint32_t fnv1a(const uint8_t *p, size_t n, uint32_t h) {
    for (size_t i = 0; i < n; i++) { h ^= p[i]; h *= 16777619u; }
    return h;
}

static void cursor_shape_poll(void) {
    @autoreleasepool {
        NSCursor *c = [NSCursor currentSystemCursor];
        if (!c || !c.image) return;
        NSSize sz = c.image.size;
        if (sz.width <= 0 || sz.height <= 0 || g_bounds.size.width <= 0 || g_pw == 0) return;
        double s = (double)g_pw / g_bounds.size.width; // native pixels per point, whatever the encoded size
        int w = (int)ceil(sz.width * s), h = (int)ceil(sz.height * s);
        if (w <= 0 || h <= 0 || w > 256 || h > 256) return;

        NSRect r = NSMakeRect(0, 0, w, h);
        CGImageRef cg = [c.image CGImageForProposedRect:&r context:nil hints:nil];
        if (!cg) return;
        size_t len = (size_t)w * h * 4;
        uint8_t *px = calloc(1, len);
        CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
        CGContextRef ctx = CGBitmapContextCreate(px, w, h, 8, w * 4, cs,
                                                 kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
        CGColorSpaceRelease(cs);
        if (!ctx) { free(px); return; }
        CGContextDrawImage(ctx, CGRectMake(0, 0, w, h), cg);
        CGContextRelease(ctx);
        for (size_t i = 0; i < len; i += 4) { // un-premultiply: the ABI carries straight alpha
            uint8_t a = px[i + 3];
            if (a != 0 && a != 255) {
                px[i] = (uint8_t)MIN(255, px[i] * 255 / a);
                px[i + 1] = (uint8_t)MIN(255, px[i + 1] * 255 / a);
                px[i + 2] = (uint8_t)MIN(255, px[i + 2] * 255 / a);
            }
        }
        uint16_t hx = (uint16_t)MIN(w - 1, MAX(0, (int)lround(c.hotSpot.x * s)));
        uint16_t hy = (uint16_t)MIN(h - 1, MAX(0, (int)lround(c.hotSpot.y * s)));
        uint32_t meta[3] = {(uint32_t)w, (uint32_t)h, ((uint32_t)hx << 16) | hy};
        uint32_t id = fnv1a(px, len, fnv1a((const uint8_t *)meta, sizeof(meta), 2166136261u));
        if (id == 0) id = 1;
        if (id != g_shape_id && atomic_load(&g_running) && g_on_shape) {
            g_shape_id = id;
            g_on_shape(g_user, id, (uint16_t)w, (uint16_t)h, hx, hy, px, (int32_t)len);
        }
        free(px);
    }
}

static void cursor_tick(void) {
    if (!atomic_load(&g_running)) return;
    CGEventRef e = CGEventCreate(NULL);
    if (!e) return;
    CGPoint p = CGEventGetLocation(e);
    CFRelease(e);
    CGRect b = g_bounds;
    int vis = CGRectContainsPoint(b, p) ? 1 : 0;
    double nx = (p.x - b.origin.x) / b.size.width, ny = (p.y - b.origin.y) / b.size.height;
    uint16_t x = (uint16_t)lround(MIN(1.0, MAX(0.0, nx)) * 65535.0);
    uint16_t y = (uint16_t)lround(MIN(1.0, MAX(0.0, ny)) * 65535.0);
    if ((x != g_pos_x || y != g_pos_y || vis != g_pos_vis) && g_on_pos) {
        g_pos_x = x; g_pos_y = y; g_pos_vis = vis;
        g_on_pos(g_user, x, y, (uint8_t)vis);
    }
    if (g_tick++ % 6 == 0) cursor_shape_poll(); // ~20 Hz shape check
}

// ---------------- content filter ----------------

// In a capture worker the agent is another process: its windows (consent,
// overlay) stay out of the picture as ours would.
static pid_t g_owner_pid;

int rc_capture_worker_init(int64_t owner_pid) {
    g_owner_pid = (pid_t)owner_pid;
    return RC_OK;
}

static BOOL own_pid(pid_t pid) { return pid == getpid() || (g_owner_pid > 0 && pid == g_owner_pid); }

// With the wallpaper hidden the filter leaves out everything below the desktop
// icons (the Dock's Wallpaper-* windows, the wallpaper agent and the
// WindowServer backdrop, one set per Space), so the background is solid black.
// Excluding windows rules out excluding our app, so our own windows are left
// out one by one and the 1 s refresh picks up new ones.
static BOOL wp_excluded(NSInteger layer, pid_t pid) {
    return layer < CGWindowLevelForKey(kCGDesktopIconWindowLevelKey) || own_pid(pid);
}

static NSSet<NSNumber *> *wp_window_ids(void) {
    NSMutableSet<NSNumber *> *ids = [NSMutableSet set];
    CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionAll, kCGNullWindowID);
    if (!list) return ids;
    for (NSDictionary *w in (__bridge NSArray *)list)
        if (wp_excluded([w[(id)kCGWindowLayer] integerValue], [w[(id)kCGWindowOwnerPID] intValue]))
            [ids addObject:w[(id)kCGWindowNumber]];
    CFRelease(list);
    return ids;
}

// Off-screen windows are listed too when hiding, so other Spaces' wallpapers
// are already excluded when the user switches to them.
static SCShareableContent *shareable_content(BOOL all_windows, NSError **err) {
    __block SCShareableContent *content = nil;
    __block NSError *cerr = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:!all_windows
                                                 completionHandler:^(SCShareableContent *c, NSError *e) {
        content = c; cerr = e; dispatch_semaphore_signal(sem);
    }];
    if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) != 0) return nil;
    if (err) *err = cerr;
    return cerr ? nil : content;
}

static SCContentFilter *make_filter(SCShareableContent *content, uint32_t display_id, BOOL hide_wp) {
    SCDisplay *disp = nil;
    for (SCDisplay *d in content.displays) if (d.displayID == display_id) disp = d;
    if (!disp) return nil;
    if (hide_wp) {
        NSMutableArray<SCWindow *> *drop = [NSMutableArray array];
        for (SCWindow *w in content.windows)
            if (wp_excluded(w.windowLayer, w.owningApplication.processID)) [drop addObject:w];
        return [[SCContentFilter alloc] initWithDisplay:disp excludingWindows:drop];
    }
    NSMutableArray *self_apps = [NSMutableArray array]; // never capture our own consent UI
    for (SCRunningApplication *app in content.applications)
        if (own_pid(app.processID)) [self_apps addObject:app];
    return [[SCContentFilter alloc] initWithDisplay:disp excludingApplications:self_apps exceptingWindows:@[]];
}

// Re-applies the filter after a toggle (force), or when the windows it must
// leave out changed: a Space switch, a new wallpaper, a window of ours.
// Caller holds g_ctl.
static void wp_refresh_locked(BOOL force) {
    if (!g_stream) return;
    BOOL hide = atomic_load(&g_hide_wp);
    NSSet<NSNumber *> *ids = hide ? wp_window_ids() : nil;
    if (!force && (!hide || [ids isEqualToSet:g_wp_ids])) return;
    SCShareableContent *content = shareable_content(hide, NULL);
    SCContentFilter *filter = content ? make_filter(content, g_display, hide) : nil;
    if (!filter) return;
    g_wp_ids = ids;
    [g_stream updateContentFilter:filter completionHandler:^(NSError *e) {
        if (e) RCLOG(@"updateContentFilter: %@", e);
    }];
}

// ---------------- lock screen ----------------

static bool session_locked(void) {
    NSDictionary *d = CFBridgingRelease(CGSessionCopyCurrentDictionary());
    id v = d[@"CGSSessionScreenIsLocked"];
    return [v respondsToSelector:@selector(boolValue)] && [v boolValue];
}

int rc_screen_locked(void) {
    @autoreleasepool { return session_locked() ? 1 : 0; }
}

bool rc_mac_lock_path(void) { return atomic_load(&g_lock_active); }

static void display_keep_awake(bool on) {
    if (on) {
        IOPMAssertionDeclareUserActivity(CFSTR("Remote session"), kIOPMUserActiveLocal, &g_activity_id);
        if (!g_awake_id)
            IOPMAssertionCreateWithName(kIOPMAssertPreventUserIdleDisplaySleep, kIOPMAssertionLevelOn,
                                        CFSTR("Remote session"), &g_awake_id);
    } else if (g_awake_id) {
        IOPMAssertionRelease(g_awake_id);
        g_awake_id = 0;
    }
}

// Caller holds g_ctl.
static void lock_stream_start_locked(void) {
    if (g_lock_stream) return;
    bool bgra = g_codec == RC_CODEC_VP9;
    CGColorSpaceRef srgb = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    NSMutableDictionary *props = [@{
        (__bridge NSString *)kCGDisplayStreamShowCursor: @NO,
        (__bridge NSString *)kCGDisplayStreamMinimumFrameTime: @(1.0 / MAX(1, g_fps)),
        (__bridge NSString *)kCGDisplayStreamQueueDepth: @4,
    } mutableCopy];
    if (bgra) props[(__bridge NSString *)kCGDisplayStreamColorSpace] = (__bridge id)srgb;
    else props[(__bridge NSString *)kCGDisplayStreamYCbCrMatrix] = (__bridge id)kCGDisplayStreamYCbCrMatrix_ITU_R_709_2;
    __block CGDisplayStreamRef s = NULL;
    s = CGDisplayStreamCreateWithDispatchQueue(
        g_display, (size_t)g_capw, (size_t)g_caph, bgra ? 'BGRA' : '420v', (__bridge CFDictionaryRef)props, g_queue,
        ^(CGDisplayStreamFrameStatus status, uint64_t t, IOSurfaceRef surf, CGDisplayStreamUpdateRef upd) {
            if (status == kCGDisplayStreamFrameStatusStopped) {
                CFRelease(s);
                return;
            }
            if (status != kCGDisplayStreamFrameStatusFrameComplete || !surf || !atomic_load(&g_running) ||
                !atomic_load(&g_lock_active))
                return;
            CVPixelBufferRef pb = NULL;
            if (CVPixelBufferCreateWithIOSurface(NULL, surf, NULL, &pb) != kCVReturnSuccess || !pb) return;
            if (g_last) CVPixelBufferRelease(g_last);
            g_last = pb;
            g_last_content_us = rc_now_us();
            encode_pixbuf(pb, atomic_exchange(&g_force_key, false), 0, 0);
        });
    CGColorSpaceRelease(srgb);
    if (!s) {
        RCLOG(@"lock screen: CGDisplayStream unavailable");
        return;
    }
    atomic_store(&g_lock_active, true);
    if (CGDisplayStreamStart(s) != kCGErrorSuccess) {
        atomic_store(&g_lock_active, false);
        CFRelease(s);
        return;
    }
    g_lock_stream = s;
}

// Caller holds g_ctl. The stream releases itself when it reports stopped.
static void lock_stream_stop_locked(void) {
    if (!g_lock_stream) return;
    atomic_store(&g_lock_active, false);
    CGDisplayStreamStop(g_lock_stream);
    g_lock_stream = NULL;
    atomic_store(&g_force_key, true); // the desktop comes back as a fresh picture
}

// RC_CAPTURE=cgds takes the unlock-screen path while unlocked too (testing).
static bool lock_path_forced(void) {
    const char *c = getenv("RC_CAPTURE");
    return c && strcmp(c, "cgds") == 0;
}

// Caller holds g_ctl.
static void lock_poll_locked(void) {
    if (!g_stream) return;
    if (session_locked() || lock_path_forced()) {
        display_keep_awake(true);
        lock_stream_start_locked();
    } else {
        lock_stream_stop_locked();
    }
}

// ---------------- public API ----------------

static void teardown_locked(void) {
    atomic_store(&g_running, false);
    atomic_store(&g_refine_ok, false);
    lock_stream_stop_locked();
    display_keep_awake(false);
    if (g_cursor_timer) {
        dispatch_source_cancel(g_cursor_timer);
        g_cursor_timer = nil;
        dispatch_sync(g_cursor_queue, ^{});
    }
    if (g_wp_timer) {
        dispatch_source_cancel(g_wp_timer);
        g_wp_timer = nil;
        dispatch_sync(g_wp_queue, ^{}); // ticks only trylock g_ctl, so this cannot deadlock
    }
    g_wp_ids = nil;
    if (g_stream) {
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [g_stream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(sem); }];
        dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
        g_stream = nil;
    }
    if (g_queue) dispatch_sync(g_queue, ^{});
    if (g_vpx_queue) dispatch_sync(g_vpx_queue, ^{});
    pthread_mutex_lock(&g_vpx_mu);
    if (g_vpx_pb) CVPixelBufferRelease(g_vpx_pb);
    g_vpx_pb = NULL;
    g_vpx_key = g_vpx_scheduled = false;
    g_vpx_qp = 0;
    pthread_mutex_unlock(&g_vpx_mu);
    if (g_vpx) {
        rc_vpx_close(g_vpx);
        g_vpx = NULL;
    }
    if (g_vt) {
        VTCompressionSessionCompleteFrames(g_vt, kCMTimeInvalid);
        VTCompressionSessionInvalidate(g_vt);
        CFRelease(g_vt);
        g_vt = NULL;
    }
    if (g_last) { CVPixelBufferRelease(g_last); g_last = NULL; }
    pthread_mutex_lock(&g_cb);
    g_on_frame = NULL; g_on_shape = NULL; g_on_pos = NULL; g_user = NULL;
    pthread_mutex_unlock(&g_cb);
    g_sc = nil;
}

static void even_fit(uint32_t pw, uint32_t ph, int maxw, int maxh, int *w, int *h) {
    double s = 1.0;
    if (maxw > 0 && pw > (uint32_t)maxw) s = MIN(s, (double)maxw / pw);
    if (maxh > 0 && ph > (uint32_t)maxh) s = MIN(s, (double)maxh / ph);
    *w = MAX(2, ((int)(pw * s + 0.5)) & ~1); // nearest: a bound the agent sized exactly stays exact
    *h = MAX(2, ((int)(ph * s + 0.5)) & ~1);
}

static int start_locked(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                        rc_cursor_pos_fn on_pos, void *user) {
    if (g_stream || atomic_load(&g_running)) return RC_ERR_BUSY;
    if (!CGPreflightScreenCaptureAccess()) return RC_ERR_PERMISSION;
    {
        CGRect b = rc_display_bounds(cfg->monitor_id);
        if (CGRectIsNull(b)) return RC_ERR;

        BOOL hide_wp = atomic_load(&g_hide_wp);
        NSSet<NSNumber *> *wp_ids = hide_wp ? wp_window_ids() : nil;
        NSError *cerr = nil;
        SCShareableContent *content = shareable_content(hide_wp, &cerr);
        if (!content) return (cerr.code == SCStreamErrorUserDeclined) ? RC_ERR_PERMISSION : RC_ERR;
        SCContentFilter *filter = make_filter(content, cfg->monitor_id, hide_wp);
        if (!filter) return RC_ERR;

        uint32_t pw = 0, ph = 0;
        rc_display_pixels(cfg->monitor_id, &pw, &ph);
        int w, h;
        even_fit(pw, ph, cfg->max_width, cfg->max_height, &w, &h);
        int fps = cfg->fps > 0 ? MIN(cfg->fps, 60) : 60;
        int kbps = cfg->bitrate_kbps > 0 ? cfg->bitrate_kbps : 4000;
        int gop = cfg->keyframe_interval_ms > 0 ? cfg->keyframe_interval_ms : 2000;

        bool full_color = cfg->codec == RC_CODEC_VP9;
        SCStreamConfiguration *sc = [[SCStreamConfiguration alloc] init];
        sc.width = w;
        sc.height = h;
        sc.minimumFrameInterval = CMTimeMake(1, fps);
        sc.queueDepth = 4;
        sc.showsCursor = NO;
        sc.capturesAudio = NO;
        if (full_color) {
            // sRGB has BT.709 primaries, so rc_vpx's matrix applies as is.
            sc.pixelFormat = kCVPixelFormatType_32BGRA;
            sc.colorSpaceName = kCGColorSpaceSRGB;
        } else {
            sc.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
            sc.colorMatrix = kCGDisplayStreamYCbCrMatrix_ITU_R_709_2;
        }

        bool refine_ok = false;
        if (full_color) {
            g_vpx = rc_vpx_open(w, h, fps, kbps);
            if (!g_vpx) return RC_ERR;
            refine_ok = true;
        } else {
            g_vt = vt_create(w, h, fps, kbps, gop, cfg->profile_idc, g_encoder, sizeof(g_encoder), &refine_ok);
            if (!g_vt) return RC_ERR;
        }
        g_codec = full_color ? RC_CODEC_VP9 : RC_CODEC_H264;
        atomic_store(&g_refine_ok, refine_ok);
        g_clamped = false;
        g_last_content_us = 0;

        if (!g_queue) {
            dispatch_queue_attr_t attr = dispatch_queue_attr_make_with_qos_class(DISPATCH_QUEUE_SERIAL,
                                                                                 QOS_CLASS_USER_INTERACTIVE, 0);
            g_queue = dispatch_queue_create("com.remotectl.capture", attr);
            g_cursor_queue = dispatch_queue_create("com.remotectl.cursor", attr);
            g_vpx_queue = dispatch_queue_create("com.remotectl.vp9", attr);
            g_wp_queue = dispatch_queue_create("com.remotectl.wallpaper", DISPATCH_QUEUE_SERIAL);
        }
        if (!g_output) g_output = [[RCStreamOutput alloc] init];

        g_display = cfg->monitor_id;
        g_wp_ids = wp_ids;
        g_bounds = b;
        g_capw = w; g_caph = h; g_fps = fps; g_kbps = kbps; g_gop_ms = gop; g_pw = pw;
        g_last_pts = 0;
        g_shape_id = 0; g_pos_x = g_pos_y = 0xFFFF; g_pos_vis = -1; g_tick = 0;
        pthread_mutex_lock(&g_cb);
        g_on_frame = on_frame; g_on_shape = on_shape; g_on_pos = on_pos; g_user = user;
        pthread_mutex_unlock(&g_cb);
        atomic_store(&g_force_key, false);
        atomic_store(&g_running, true);

        g_sc = sc;
        g_stream = [[SCStream alloc] initWithFilter:filter configuration:sc delegate:g_output];
        NSError *aerr = nil;
        if (![g_stream addStreamOutput:g_output type:SCStreamOutputTypeScreen sampleHandlerQueue:g_queue error:&aerr]) {
            teardown_locked();
            return RC_ERR;
        }
        __block NSError *serr = nil;
        dispatch_semaphore_t ssem = dispatch_semaphore_create(0);
        [g_stream startCaptureWithCompletionHandler:^(NSError *e) { serr = e; dispatch_semaphore_signal(ssem); }];
        if (dispatch_semaphore_wait(ssem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) != 0 || serr) {
            teardown_locked();
            return (serr.code == SCStreamErrorUserDeclined) ? RC_ERR_PERMISSION : RC_ERR;
        }

        if (on_shape || on_pos) {
            g_cursor_timer = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, g_cursor_queue);
            dispatch_source_set_timer(g_cursor_timer, DISPATCH_TIME_NOW, NSEC_PER_SEC / 120, NSEC_PER_MSEC);
            dispatch_source_set_event_handler(g_cursor_timer, ^{ cursor_tick(); });
            dispatch_resume(g_cursor_timer);
        }
        g_wp_timer = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, g_wp_queue);
        dispatch_source_set_timer(g_wp_timer, dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC), NSEC_PER_SEC,
                                  100 * NSEC_PER_MSEC);
        dispatch_source_set_event_handler(g_wp_timer, ^{
            if (pthread_mutex_trylock(&g_ctl) != 0) return;
            @autoreleasepool {
                lock_poll_locked();
                if (atomic_load(&g_hide_wp)) wp_refresh_locked(NO);
            }
            pthread_mutex_unlock(&g_ctl);
        });
        dispatch_resume(g_wp_timer);
        display_keep_awake(true);
        lock_poll_locked();
        return RC_OK;
    }
}

int rc_capture_start(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                     rc_cursor_pos_fn on_pos, void *user) {
    if (!cfg || !on_frame) return RC_ERR;
    pthread_mutex_lock(&g_ctl);
    int rc;
    @autoreleasepool { rc = start_locked(cfg, on_frame, on_shape, on_pos, user); }
    pthread_mutex_unlock(&g_ctl);
    return rc;
}

void rc_capture_stop(void) {
    pthread_mutex_lock(&g_ctl);
    if (g_stream || atomic_load(&g_running) || g_vt) {
        @autoreleasepool { teardown_locked(); }
    }
    pthread_mutex_unlock(&g_ctl);
}

void rc_encode_configure(int32_t bitrate_kbps, int32_t fps) {
    pthread_mutex_lock(&g_ctl);
    if (atomic_load(&g_running) && (g_vt || g_vpx)) {
        int kbps = bitrate_kbps > 0 ? bitrate_kbps : g_kbps;
        int f = fps > 0 ? MIN(fps, 60) : g_fps;
        BOOL fps_changed = f != g_fps;
        g_kbps = kbps;
        g_fps = f;
        if (g_vpx) dispatch_async(g_vpx_queue, ^{ if (g_vpx) rc_vpx_rate(g_vpx, kbps, f); });
        else dispatch_async(g_queue, ^{ if (g_vt) vt_set_rate(g_vt, kbps, f); });
        if (fps_changed && g_stream && g_sc) {
            g_sc.minimumFrameInterval = CMTimeMake(1, f);
            [g_stream updateConfiguration:g_sc completionHandler:^(NSError *e) {
                if (e) RCLOG(@"updateConfiguration: %@", e);
            }];
        }
    }
    pthread_mutex_unlock(&g_ctl);
}

void rc_request_keyframe(void) {
    if (!atomic_load(&g_running) || !g_queue) return;
    atomic_store(&g_force_key, true);
    // A static screen produces no new frames, so re-encode the last one.
    dispatch_async(g_queue, ^{
        if (atomic_load(&g_running) && g_last && atomic_exchange(&g_force_key, false))
            encode_pixbuf(g_last, true, 0, 0);
    });
}

int rc_encode_refine(int32_t max_qp) {
    if (!atomic_load(&g_running) || !g_queue) return RC_ERR;
    if (max_qp > 0 && !atomic_load(&g_refine_ok)) return RC_ERR_UNSUPPORTED;
    int qp = max_qp > 0 ? MIN(51, max_qp) : 0; // 0: a repeat pass
    dispatch_async(g_queue, ^{
        if (atomic_load(&g_running) && g_last && rc_now_us() - g_last_content_us >= 100000)
            encode_pixbuf(g_last, false, qp, RC_FRAME_STATIC);
    });
    return RC_OK;
}

int rc_wallpaper_hide(int hide) {
    BOOL h = hide != 0;
    if (atomic_exchange(&g_hide_wp, h) == h) return RC_OK;
    pthread_mutex_lock(&g_ctl);
    @autoreleasepool { wp_refresh_locked(YES); }
    pthread_mutex_unlock(&g_ctl);
    return RC_OK;
}

int rc_codecs(void) { return 1 << RC_CODEC_H264 | 1 << RC_CODEC_VP9; }

const char *rc_encoder_name(void) {
    pthread_mutex_lock(&g_ctl);
    if (atomic_load(&g_running) && g_codec == RC_CODEC_VP9) {
        pthread_mutex_unlock(&g_ctl);
        return "libvpx-vp9";
    }
    if (g_encoder[0] == 0) {
        bool refine_ok = false;
        VTCompressionSessionRef s = vt_create(1280, 720, 60, 4000, 2000, 66, g_encoder, sizeof(g_encoder), &refine_ok);
        if (s) { VTCompressionSessionInvalidate(s); CFRelease(s); }
        else strlcpy(g_encoder, "none", sizeof(g_encoder));
    }
    pthread_mutex_unlock(&g_ctl);
    return g_encoder;
}

// ---------------- preview (rc_snapshot) ----------------

// macOS 13: one frame from a short-lived SCStream.
@interface RCSnapshotOutput : NSObject <SCStreamOutput>
@property(nonatomic) CVPixelBufferRef frame;
@property(nonatomic, strong) dispatch_semaphore_t done;
@end

@implementation RCSnapshotOutput
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen || self.frame) return;
    CFArrayRef att = CMSampleBufferGetSampleAttachmentsArray(sb, false);
    if (att && CFArrayGetCount(att) > 0) {
        NSDictionary *info = (__bridge NSDictionary *)CFArrayGetValueAtIndex(att, 0);
        NSNumber *status = info[SCStreamFrameInfoStatus];
        if (status && status.integerValue != SCFrameStatusComplete) return;
    }
    CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sb);
    if (!pb) return;
    self.frame = CVPixelBufferRetain(pb);
    dispatch_semaphore_signal(self.done);
}
@end

// Copies a BGRA pixel buffer into a tight w×h buffer (cropping if larger).
static uint8_t *copy_pixbuf(CVPixelBufferRef pb, int32_t w, int32_t h) {
    if (CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) return NULL;
    int32_t pw = (int32_t)CVPixelBufferGetWidth(pb), ph = (int32_t)CVPixelBufferGetHeight(pb);
    size_t stride = CVPixelBufferGetBytesPerRow(pb);
    const uint8_t *base = CVPixelBufferGetBaseAddress(pb);
    uint8_t *out = calloc((size_t)w * h, 4);
    if (out && base) {
        int32_t cw = MIN(w, pw), ch = MIN(h, ph);
        for (int32_t y = 0; y < ch; y++) memcpy(out + (size_t)y * w * 4, base + y * stride, (size_t)cw * 4);
    }
    CVPixelBufferUnlockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
    return out;
}

// Draws a CGImage into a tight BGRA w×h buffer.
static uint8_t *copy_cgimage(CGImageRef img, int32_t w, int32_t h) {
    uint8_t *out = calloc((size_t)w * h, 4);
    if (!out) return NULL;
    CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    CGContextRef ctx = CGBitmapContextCreate(out, (size_t)w, (size_t)h, 8, (size_t)w * 4, cs,
                                             kCGImageAlphaPremultipliedFirst | kCGBitmapByteOrder32Little);
    CGColorSpaceRelease(cs);
    if (!ctx) {
        free(out);
        return NULL;
    }
    CGContextSetInterpolationQuality(ctx, kCGInterpolationHigh);
    CGContextDrawImage(ctx, CGRectMake(0, 0, w, h), img);
    CGContextRelease(ctx);
    return out;
}

// Caller holds g_ctl: a capture cannot start meanwhile.
static int snapshot_locked(uint32_t monitor_id, int32_t max_w, int32_t max_h, rc_image *out) {
    if (atomic_load(&g_running) || g_stream) return RC_ERR_BUSY;
    if (!CGPreflightScreenCaptureAccess()) return RC_ERR_PERMISSION; // never prompts
    if (session_locked()) return RC_ERR_LOCKED;
    uint32_t did = monitor_id ? monitor_id : CGMainDisplayID();
    if (CGDisplayIsAsleep(did)) return RC_ERR_ASLEEP;
    SCShareableContent *content = shareable_content(NO, NULL);
    SCContentFilter *filter = content ? make_filter(content, did, NO) : nil;
    if (!filter) return RC_ERR;
    uint32_t pw = 0, ph = 0;
    rc_display_pixels(did, &pw, &ph);
    int32_t w, h;
    rc_fit((int32_t)pw, (int32_t)ph, max_w, max_h, &w, &h);

    SCStreamConfiguration *sc = [SCStreamConfiguration new];
    sc.width = (size_t)w; // the GPU scales
    sc.height = (size_t)h;
    sc.showsCursor = NO;
    sc.pixelFormat = kCVPixelFormatType_32BGRA;
    sc.queueDepth = 3;

    uint8_t *px = NULL;
    if (@available(macOS 14.0, *)) {
        __block CGImageRef img = NULL;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [SCScreenshotManager captureImageWithFilter:filter configuration:sc
                                  completionHandler:^(CGImageRef image, NSError *e) {
            if (image && !e) img = CGImageRetain(image);
            dispatch_semaphore_signal(sem);
        }];
        if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC)) != 0) return RC_ERR;
        if (!img) return RC_ERR;
        px = copy_cgimage(img, w, h);
        CGImageRelease(img);
    } else {
        RCSnapshotOutput *o = [RCSnapshotOutput new];
        o.done = dispatch_semaphore_create(0);
        sc.minimumFrameInterval = CMTimeMake(1, 30);
        SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:sc delegate:nil];
        dispatch_queue_t q = dispatch_queue_create("rc.snapshot", DISPATCH_QUEUE_SERIAL);
        if (![stream addStreamOutput:o type:SCStreamOutputTypeScreen sampleHandlerQueue:q error:NULL]) return RC_ERR;
        dispatch_semaphore_t started = dispatch_semaphore_create(0);
        __block BOOL ok = NO;
        [stream startCaptureWithCompletionHandler:^(NSError *e) {
            ok = e == nil;
            dispatch_semaphore_signal(started);
        }];
        dispatch_semaphore_wait(started, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
        if (ok) dispatch_semaphore_wait(o.done, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
        dispatch_semaphore_t stopped = dispatch_semaphore_create(0);
        [stream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(stopped); }];
        dispatch_semaphore_wait(stopped, dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC));
        dispatch_sync(q, ^{}); // the handler is done with o
        if (!o.frame) return RC_ERR;
        px = copy_pixbuf(o.frame, w, h);
        CVPixelBufferRelease(o.frame);
        o.frame = NULL;
    }
    if (!px) return RC_ERR;
    for (size_t i = 3; i < (size_t)w * h * 4; i += 4) px[i] = 255;
    out->pixels = px;
    out->width = w;
    out->height = h;
    out->stride = w * 4;
    return RC_OK;
}

int rc_snapshot(uint32_t monitor_id, int32_t max_w, int32_t max_h, rc_image *out) {
    if (!out) return RC_ERR;
    memset(out, 0, sizeof *out);
    pthread_mutex_lock(&g_ctl);
    int rc;
    @autoreleasepool { rc = snapshot_locked(monitor_id, max_w, max_h, out); }
    pthread_mutex_unlock(&g_ctl);
    return rc;
}
