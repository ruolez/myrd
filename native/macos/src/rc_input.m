// Input injection through CGEvent, posted at the HID tap.
#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#import <Carbon/Carbon.h>
#include <pthread.h>
#include "rc_internal.h"

static pthread_mutex_t g_in = PTHREAD_MUTEX_INITIALIZER;
static CGEventSourceRef g_src;
static CGPoint g_pos;
static BOOL g_pos_valid;
static uint32_t g_buttons;     // bit per RC_BTN_*
static CGEventFlags g_mods;    // tracked from injected modifier keys
static uint32_t g_mod_down;    // bit per modifier key below
static BOOL g_caps;

// Double/triple click detection: macOS apps read kCGMouseEventClickState.
static int g_click_btn = -1;
static int64_t g_click_ts;
static CGPoint g_click_pos;
static int g_click_count;

static CGEventSourceRef src(void) {
    if (!g_src) {
        g_src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
        if (g_src) CGEventSourceSetLocalEventsSuppressionInterval(g_src, 0.0);
    }
    return g_src;
}

static CGPoint current_pos(void) {
    if (g_pos_valid) return g_pos;
    CGEventRef e = CGEventCreate(NULL);
    CGPoint p = e ? CGEventGetLocation(e) : CGPointZero;
    if (e) CFRelease(e);
    return p;
}

static CGPoint clamp_to_displays(CGPoint p) {
    CGDirectDisplayID ids[32];
    uint32_t n = 0;
    if (CGGetActiveDisplayList(32, ids, &n) != kCGErrorSuccess || n == 0) return p;
    for (uint32_t i = 0; i < n; i++)
        if (CGRectContainsPoint(CGDisplayBounds(ids[i]), p)) return p;
    // Outside every display: clamp into the main display.
    CGRect b = CGDisplayBounds(CGMainDisplayID());
    p.x = MIN(MAX(p.x, CGRectGetMinX(b)), CGRectGetMaxX(b) - 1);
    p.y = MIN(MAX(p.y, CGRectGetMinY(b)), CGRectGetMaxY(b) - 1);
    return p;
}

static CGMouseButton cg_button(uint8_t b) {
    switch (b) {
        case RC_BTN_LEFT: return kCGMouseButtonLeft;
        case RC_BTN_RIGHT: return kCGMouseButtonRight;
        case RC_BTN_MIDDLE: return kCGMouseButtonCenter;
        default: return (CGMouseButton)b; // back = 3, forward = 4
    }
}

static CGEventType move_type(void) {
    if (g_buttons & (1u << RC_BTN_LEFT)) return kCGEventLeftMouseDragged;
    if (g_buttons & (1u << RC_BTN_RIGHT)) return kCGEventRightMouseDragged;
    if (g_buttons) return kCGEventOtherMouseDragged;
    return kCGEventMouseMoved;
}

static CGMouseButton move_button(void) {
    if (g_buttons & (1u << RC_BTN_LEFT)) return kCGMouseButtonLeft;
    if (g_buttons & (1u << RC_BTN_RIGHT)) return kCGMouseButtonRight;
    for (uint8_t b = RC_BTN_MIDDLE; b <= RC_BTN_FORWARD; b++)
        if (g_buttons & (1u << b)) return cg_button(b);
    return kCGMouseButtonLeft;
}

static int post(CGEventRef e) {
    if (!e) return RC_ERR;
    CGEventPost(kCGHIDEventTap, e);
    CFRelease(e);
    return RC_OK;
}

static int post_move(CGPoint p, int16_t dx, int16_t dy) {
    g_pos = p;
    g_pos_valid = YES;
    CGEventRef e = CGEventCreateMouseEvent(src(), move_type(), p, move_button());
    if (!e) return RC_ERR;
    if (dx || dy) {
        CGEventSetIntegerValueField(e, kCGMouseEventDeltaX, dx);
        CGEventSetIntegerValueField(e, kCGMouseEventDeltaY, dy);
    }
    CGEventSetFlags(e, g_mods);
    return post(e);
}

static int post_button(uint8_t b, BOOL down) {
    if (b > RC_BTN_FORWARD) return RC_ERR;
    CGPoint p = current_pos();
    CGEventType t;
    if (b == RC_BTN_LEFT) t = down ? kCGEventLeftMouseDown : kCGEventLeftMouseUp;
    else if (b == RC_BTN_RIGHT) t = down ? kCGEventRightMouseDown : kCGEventRightMouseUp;
    else t = down ? kCGEventOtherMouseDown : kCGEventOtherMouseUp;

    if (down) {
        int64_t now = rc_now_us();
        double interval = [NSEvent doubleClickInterval] * 1e6;
        if (g_click_btn == b && now - g_click_ts <= interval && fabs(p.x - g_click_pos.x) <= 4 &&
            fabs(p.y - g_click_pos.y) <= 4)
            g_click_count = MIN(g_click_count + 1, 3);
        else
            g_click_count = 1;
        g_click_btn = b;
        g_click_ts = now;
        g_click_pos = p;
        g_buttons |= (1u << b);
    } else {
        g_buttons &= ~(1u << b);
    }
    CGEventRef e = CGEventCreateMouseEvent(src(), t, p, cg_button(b));
    if (!e) return RC_ERR;
    CGEventSetIntegerValueField(e, kCGMouseEventClickState, g_click_btn == b ? g_click_count : 1);
    CGEventSetFlags(e, g_mods);
    return post(e);
}

// Modifier key → (flag, bit in g_mod_down). Left/right share the device-independent flag.
typedef struct { uint16_t vk; CGEventFlags flag; uint32_t bit; } modkey;
static const modkey kMods[] = {
    {kVK_Shift, kCGEventFlagMaskShift, 1u << 0},      {kVK_RightShift, kCGEventFlagMaskShift, 1u << 1},
    {kVK_Control, kCGEventFlagMaskControl, 1u << 2},  {kVK_RightControl, kCGEventFlagMaskControl, 1u << 3},
    {kVK_Option, kCGEventFlagMaskAlternate, 1u << 4}, {kVK_RightOption, kCGEventFlagMaskAlternate, 1u << 5},
    {kVK_Command, kCGEventFlagMaskCommand, 1u << 6},  {kVK_RightCommand, kCGEventFlagMaskCommand, 1u << 7},
    {kVK_Function, kCGEventFlagMaskSecondaryFn, 1u << 8},
};

static void recompute_mods(void) {
    CGEventFlags f = 0;
    for (size_t i = 0; i < sizeof(kMods) / sizeof(kMods[0]); i++)
        if (g_mod_down & kMods[i].bit) f |= kMods[i].flag;
    if (g_caps) f |= kCGEventFlagMaskAlphaShift;
    g_mods = f;
}

static BOOL is_keypad(uint32_t vk) {
    return (vk >= kVK_ANSI_Keypad0 && vk <= kVK_ANSI_Keypad9) || vk == kVK_ANSI_KeypadDecimal ||
           vk == kVK_ANSI_KeypadMultiply || vk == kVK_ANSI_KeypadPlus || vk == kVK_ANSI_KeypadClear ||
           vk == kVK_ANSI_KeypadDivide || vk == kVK_ANSI_KeypadEnter || vk == kVK_ANSI_KeypadMinus ||
           vk == kVK_ANSI_KeypadEquals;
}

static BOOL is_arrow_or_nav(uint32_t vk) {
    return (vk >= kVK_LeftArrow && vk <= kVK_UpArrow) || vk == kVK_Home || vk == kVK_End || vk == kVK_PageUp ||
           vk == kVK_PageDown || vk == kVK_ForwardDelete || vk == kVK_Help;
}

static int post_key(uint32_t vk, BOOL down) {
    if (vk > 0x7F) return RC_ERR;
    for (size_t i = 0; i < sizeof(kMods) / sizeof(kMods[0]); i++) {
        if (kMods[i].vk == vk) {
            if (down) g_mod_down |= kMods[i].bit;
            else g_mod_down &= ~kMods[i].bit;
            break;
        }
    }
    if (vk == kVK_CapsLock && down) g_caps = !g_caps;
    recompute_mods();

    CGEventRef e = CGEventCreateKeyboardEvent(src(), (CGKeyCode)vk, down);
    if (!e) return RC_ERR;
    CGEventFlags f = g_mods;
    if (is_keypad(vk)) f |= kCGEventFlagMaskNumericPad;
    if (is_arrow_or_nav(vk)) f |= kCGEventFlagMaskSecondaryFn | (vk >= kVK_LeftArrow ? kCGEventFlagMaskNumericPad : 0);
    CGEventSetFlags(e, f);
    return post(e);
}

int rc_input_inject(const rc_input_event *ev) {
    if (!ev) return RC_ERR;
    if (!AXIsProcessTrusted()) return RC_ERR_PERMISSION;
    int rc = RC_ERR;
    pthread_mutex_lock(&g_in);
    switch (ev->type) {
        case RC_EV_MOUSE_MOVE: {
            CGRect b = rc_display_bounds(ev->monitor_id);
            if (CGRectIsNull(b)) b = CGDisplayBounds(CGMainDisplayID());
            CGPoint p = CGPointMake(b.origin.x + (ev->x / 65535.0) * (b.size.width - 1),
                                    b.origin.y + (ev->y / 65535.0) * (b.size.height - 1));
            rc = post_move(p, 0, 0);
            break;
        }
        case RC_EV_MOUSE_MOVE_REL: {
            CGPoint p = current_pos();
            p.x += ev->dx;
            p.y += ev->dy;
            rc = post_move(clamp_to_displays(p), ev->dx, ev->dy);
            break;
        }
        case RC_EV_MOUSE_BUTTON:
            rc = post_button(ev->button, ev->down != 0);
            break;
        case RC_EV_MOUSE_WHEEL: {
            // DOM: dy > 0 scrolls content down; CG pixel units: negative wheel1 scrolls down.
            CGEventRef e = CGEventCreateScrollWheelEvent2(src(), kCGScrollEventUnitPixel, 2, -ev->dy, -ev->dx, 0);
            if (e) {
                CGEventSetLocation(e, current_pos());
                CGEventSetFlags(e, g_mods);
            }
            rc = post(e);
            break;
        }
        case RC_EV_KEY:
            rc = post_key(ev->keycode, ev->down != 0);
            break;
        default:
            rc = RC_ERR;
    }
    pthread_mutex_unlock(&g_in);
    return rc;
}

int rc_input_text(const char *utf8, int32_t len) {
    if (!utf8 || len <= 0) return RC_ERR;
    if (!AXIsProcessTrusted()) return RC_ERR_PERMISSION;
    @autoreleasepool {
        NSString *s = [[NSString alloc] initWithBytes:utf8 length:(NSUInteger)len encoding:NSUTF8StringEncoding];
        if (!s) return RC_ERR;
        NSUInteger n = s.length;
        unichar buf[20];
        pthread_mutex_lock(&g_in);
        // CGEventKeyboardSetUnicodeString accepts at most 20 UTF-16 units per event;
        // never split a surrogate pair across events.
        for (NSUInteger i = 0; i < n;) {
            NSUInteger k = MIN((NSUInteger)20, n - i);
            if (k < n - i && CFStringIsSurrogateHighCharacter([s characterAtIndex:i + k - 1])) k--;
            [s getCharacters:buf range:NSMakeRange(i, k)];
            for (int down = 1; down >= 0; down--) {
                CGEventRef e = CGEventCreateKeyboardEvent(src(), 0, down);
                if (!e) continue;
                CGEventSetFlags(e, 0);
                CGEventKeyboardSetUnicodeString(e, k, buf);
                post(e);
            }
            i += k;
        }
        pthread_mutex_unlock(&g_in);
    }
    return RC_OK;
}

// Releases every button and modifier this backend pressed (session end).
void rc_input_reset(void) {
    pthread_mutex_lock(&g_in);
    BOOL trusted = AXIsProcessTrusted();
    for (uint8_t b = 0; b <= RC_BTN_FORWARD && trusted; b++)
        if (g_buttons & (1u << b)) post_button(b, NO);
    for (size_t i = 0; i < sizeof(kMods) / sizeof(kMods[0]) && trusted; i++)
        if (g_mod_down & kMods[i].bit) post_key(kMods[i].vk, NO);
    g_buttons = 0;
    g_mod_down = 0;
    g_caps = NO;
    g_pos_valid = NO;
    recompute_mods();
    pthread_mutex_unlock(&g_in);
}
