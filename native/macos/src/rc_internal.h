// Internal helpers shared by the macOS backend translation units.
#ifndef RC_INTERNAL_H
#define RC_INTERNAL_H

#import <Foundation/Foundation.h>
#import <CoreGraphics/CoreGraphics.h>
#include "rc_native.h"

#ifdef RC_DEBUG
#define RCLOG(...) NSLog(@"[rcnative] " __VA_ARGS__)
#else
#define RCLOG(...) do {} while (0)
#endif

// Display bounds in global points (top-left origin), or CGRectNull.
CGRect rc_display_bounds(uint32_t display_id);
// Pixel size of a display's current mode.
void rc_display_pixels(uint32_t display_id, uint32_t *w, uint32_t *h);

// Monotonic microseconds (host clock).
int64_t rc_now_us(void);

// Runs block on the main queue only if the main thread is servicing its run
// loop; returns NO (block not scheduled) when it is not, so callers never
// block forever waiting on a main thread that Go is holding.
BOOL rc_main_loop_alive(void);

void rc_input_reset(void);

// The capture takes its pictures from CGDisplayStream (the unlock-screen path).
bool rc_mac_lock_path(void);

#endif
