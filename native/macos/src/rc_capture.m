// Capture (ScreenCaptureKit) → hardware H.264 (VideoToolbox) → Annex-B
// access units, plus the out-of-band cursor stream.
#import <AppKit/AppKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <VideoToolbox/VideoToolbox.h>
#import <CoreMedia/CoreMedia.h>
#include <pthread.h>
#include <stdatomic.h>
#include <string.h>
#include <unistd.h>
#include "rc_internal.h"

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
static VTCompressionSessionRef g_vt;
static CVPixelBufferRef g_last;
static int64_t g_last_pts;

static uint32_t g_display;
static CGRect g_bounds;
static int g_capw, g_caph, g_fps, g_kbps, g_gop_ms;
static char g_encoder[32];

static uint8_t *g_out;
static size_t g_out_cap;

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

    if (n > ps_bytes) g_on_frame(g_user, g_out, (int32_t)n, pts_us, key ? 1 : 0);
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
// (constrained baseline), plain hardware (main), software (main).
static VTCompressionSessionRef vt_create(int w, int h, int fps, int kbps, int gop_ms, char *name, size_t name_cap) {
    NSDictionary *ll = @{(__bridge NSString *)kVTVideoEncoderSpecification_EnableLowLatencyRateControl: @YES};
    NSDictionary *hw = @{(__bridge NSString *)kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder: @YES};
    struct { NSDictionary *spec; CFStringRef profile; } attempts[] = {
        {ll, kVTProfileLevel_H264_ConstrainedBaseline_AutoLevel},
        {hw, kVTProfileLevel_H264_Main_AutoLevel},
        {nil, kVTProfileLevel_H264_Main_AutoLevel},
    };
    for (size_t i = 0; i < sizeof(attempts) / sizeof(attempts[0]); i++) {
        VTCompressionSessionRef s = NULL;
        OSStatus st = VTCompressionSessionCreate(kCFAllocatorDefault, w, h, kCMVideoCodecType_H264,
                                                 (__bridge CFDictionaryRef)attempts[i].spec, NULL, NULL,
                                                 vt_output, NULL, &s);
        if (st != noErr || s == NULL) continue;
        VTSessionSetProperty(s, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_ProfileLevel, attempts[i].profile);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration,
                             (__bridge CFNumberRef)@(gop_ms / 1000.0));
        VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxKeyFrameInterval,
                             (__bridge CFNumberRef)@(MAX(1, fps * gop_ms / 1000)));
        VTSessionSetProperty(s, kVTCompressionPropertyKey_MaxFrameDelayCount, (__bridge CFNumberRef)@0);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_ColorPrimaries, kCMFormatDescriptionColorPrimaries_ITU_R_709_2);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_TransferFunction, kCMFormatDescriptionTransferFunction_ITU_R_709_2);
        VTSessionSetProperty(s, kVTCompressionPropertyKey_YCbCrMatrix, kCMFormatDescriptionYCbCrMatrix_ITU_R_709_2);
        vt_set_rate(s, kbps, fps);
        VTCompressionSessionPrepareToEncodeFrames(s);

        // The low-latency encoder (rtvc) is hardware-only and does not answer this query.
        CFBooleanRef using_hw = NULL;
        OSStatus qs = VTSessionCopyProperty(s, kVTCompressionPropertyKey_UsingHardwareAcceleratedVideoEncoder,
                                            kCFAllocatorDefault, &using_hw);
        BOOL hardware = qs == noErr ? (using_hw && CFBooleanGetValue(using_hw)) : (attempts[i].spec == ll);
        if (using_hw) CFRelease(using_hw);
        strlcpy(name, hardware ? "videotoolbox" : "videotoolbox-sw", name_cap);
        RCLOG(@"encoder attempt %zu ok, hardware=%d", i, hardware);
        return s;
    }
    return NULL;
}

static void encode_pixbuf(CVPixelBufferRef pb, bool force_key) {
    if (!g_vt || !pb) return;
    int64_t now = rc_now_us();
    if (now <= g_last_pts) now = g_last_pts + 1;
    g_last_pts = now;
    NSDictionary *opts = force_key ? @{(__bridge NSString *)kVTEncodeFrameOptionKey_ForceKeyFrame: @YES} : nil;
    VTCompressionSessionEncodeFrame(g_vt, pb, CMTimeMake(now, 1000000), kCMTimeInvalid,
                                    (__bridge CFDictionaryRef)opts, NULL, NULL);
}

// ---------------- stream output ----------------

@implementation RCStreamOutput
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen || !atomic_load(&g_running)) return;
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
    encode_pixbuf(pb, atomic_exchange(&g_force_key, false));
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
        if (sz.width <= 0 || sz.height <= 0 || g_bounds.size.width <= 0) return;
        double s = (double)g_capw / g_bounds.size.width; // video pixels per point
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

// ---------------- public API ----------------

static void teardown_locked(void) {
    atomic_store(&g_running, false);
    if (g_cursor_timer) {
        dispatch_source_cancel(g_cursor_timer);
        g_cursor_timer = nil;
        dispatch_sync(g_cursor_queue, ^{});
    }
    if (g_stream) {
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [g_stream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(sem); }];
        dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
        g_stream = nil;
    }
    if (g_queue) dispatch_sync(g_queue, ^{});
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
    *w = MAX(2, ((int)(pw * s)) & ~1);
    *h = MAX(2, ((int)(ph * s)) & ~1);
}

static int start_locked(const rc_capture_config *cfg, rc_frame_fn on_frame, rc_cursor_shape_fn on_shape,
                        rc_cursor_pos_fn on_pos, void *user) {
    if (g_stream || atomic_load(&g_running)) return RC_ERR_BUSY;
    if (!CGPreflightScreenCaptureAccess()) return RC_ERR_PERMISSION;
    {
        CGRect b = rc_display_bounds(cfg->monitor_id);
        if (CGRectIsNull(b)) return RC_ERR;

        __block SCShareableContent *content = nil;
        __block NSError *cerr = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES
                                                     completionHandler:^(SCShareableContent *c, NSError *e) {
            content = c; cerr = e; dispatch_semaphore_signal(sem);
        }];
        if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) != 0) return RC_ERR;
        if (cerr || !content) {
            return (cerr.code == SCStreamErrorUserDeclined) ? RC_ERR_PERMISSION : RC_ERR;
        }
        SCDisplay *disp = nil;
        for (SCDisplay *d in content.displays) if (d.displayID == cfg->monitor_id) disp = d;
        if (!disp) return RC_ERR;
        NSMutableArray *self_apps = [NSMutableArray array]; // never capture our own consent UI
        for (SCRunningApplication *app in content.applications)
            if (app.processID == getpid()) [self_apps addObject:app];
        SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:disp excludingApplications:self_apps
                                                          exceptingWindows:@[]];

        uint32_t pw = 0, ph = 0;
        rc_display_pixels(cfg->monitor_id, &pw, &ph);
        int w, h;
        even_fit(pw, ph, cfg->max_width, cfg->max_height, &w, &h);
        int fps = cfg->fps > 0 ? MIN(cfg->fps, 60) : 60;
        int kbps = cfg->bitrate_kbps > 0 ? cfg->bitrate_kbps : 4000;
        int gop = cfg->keyframe_interval_ms > 0 ? cfg->keyframe_interval_ms : 2000;

        SCStreamConfiguration *sc = [[SCStreamConfiguration alloc] init];
        sc.width = w;
        sc.height = h;
        sc.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
        sc.minimumFrameInterval = CMTimeMake(1, fps);
        sc.queueDepth = 4;
        sc.showsCursor = NO;
        sc.colorMatrix = kCGDisplayStreamYCbCrMatrix_ITU_R_709_2;
        sc.capturesAudio = NO;

        g_vt = vt_create(w, h, fps, kbps, gop, g_encoder, sizeof(g_encoder));
        if (!g_vt) return RC_ERR;

        if (!g_queue) {
            dispatch_queue_attr_t attr = dispatch_queue_attr_make_with_qos_class(DISPATCH_QUEUE_SERIAL,
                                                                                 QOS_CLASS_USER_INTERACTIVE, 0);
            g_queue = dispatch_queue_create("com.remotectl.capture", attr);
            g_cursor_queue = dispatch_queue_create("com.remotectl.cursor", attr);
        }
        if (!g_output) g_output = [[RCStreamOutput alloc] init];

        g_display = cfg->monitor_id;
        g_bounds = b;
        g_capw = w; g_caph = h; g_fps = fps; g_kbps = kbps; g_gop_ms = gop;
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
    if (atomic_load(&g_running) && g_vt) {
        int kbps = bitrate_kbps > 0 ? bitrate_kbps : g_kbps;
        int f = fps > 0 ? MIN(fps, 60) : g_fps;
        BOOL fps_changed = f != g_fps;
        g_kbps = kbps;
        g_fps = f;
        dispatch_async(g_queue, ^{ if (g_vt) vt_set_rate(g_vt, kbps, f); });
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
            encode_pixbuf(g_last, true);
    });
}

const char *rc_encoder_name(void) {
    pthread_mutex_lock(&g_ctl);
    if (g_encoder[0] == 0) {
        VTCompressionSessionRef s = vt_create(1280, 720, 60, 4000, 2000, g_encoder, sizeof(g_encoder));
        if (s) { VTCompressionSessionInvalidate(s); CFRelease(s); }
        else strlcpy(g_encoder, "none", sizeof(g_encoder));
    }
    pthread_mutex_unlock(&g_ctl);
    return g_encoder;
}
