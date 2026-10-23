package session

import (
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Static-screen refinement: once nothing on the screen has changed for
// refineIdle, the encoder re-encodes the last frame in passes of falling
// quantizer until the text is sharp (Chrome Remote Desktop's approach). Each
// pass is paced so refinement takes at most refineShare of the target bitrate.
// An encoder that cannot bound its quantizer (some Windows hardware MFTs)
// gets repeat passes instead: the same picture again under the normal rate
// control, each one correcting what the last left, until one comes back
// small. Without them its first, coarse frame of a still screen stayed.

var refineQPs = []int{36, 33, 30, 27, 24, 21, 18}

const (
	refineIdle   = 200 * time.Millisecond
	refineMinGap = 100 * time.Millisecond
	refineLost   = time.Second // a pass that never came back: new content won
	refineShare  = 0.5
	refineTick   = 25 * time.Millisecond
	// A change to a small area (a caret, a clock: FrameMinor, or a content
	// frame under refineSmall from a backend that cannot tell) leaves the
	// rest of the screen refined: the passes still to come cover it, and a
	// finished refinement sends its finest pass again.
	refineSmall = 4096

	refineRepeatMax  = 24   // repeat passes at most
	refineRepeatRedo = 3    // repeat passes a small change can win back
	refineSettled    = 1500 // a repeat pass this small changed next to nothing: converged
)

// refineCap bounds the passes sent since the last real content frame (new
// content or a keyframe, neither a pass's output): a full ladder plus as many
// again for the passes small changes redo. Whatever the flags an encoder
// reports, a still screen then stops costing frames.
func refineCap(repeat bool) int {
	if repeat {
		return 2 * refineRepeatMax
	}
	return 2 * len(refineQPs)
}

type refiner struct {
	pass    int  // next index into refineQPs, or repeat passes sent
	repeat  bool // repeat passes (quantizer 0) instead of refineQPs
	pending bool // a pass is out and may still come back (until refineLost)
	await   bool // a pass was requested and no frame has arrived since
	sent    int  // passes since the last real content frame (refineCap)
	sentAt  time.Time
	next    time.Time // earliest next pass; zero until the first frame
}

// onFrame records an encoded frame of size bytes and reports whether
// refinement starts over: on new content, or a keyframe, which the rate
// control encodes coarsely again. A small change waits for a quiet screen
// (never sooner than the pacing allows) and redoes at most the finest pass.
// The first frame after a pass was requested is that pass, whatever its
// flags: an encoder may lose the static flag (Windows matches it back by
// sample time) or answer with a keyframe, and either must not look like new
// content, or a still screen would restart its passes forever.
func (r *refiner) onFrame(now time.Time, size int, f platform.FrameFlags, kbps int) bool {
	if r.await {
		f = platform.FrameStatic
	}
	r.await = false
	static := f&platform.FrameStatic != 0
	minor := f&platform.FrameMinor != 0 || size < refineSmall
	switch {
	case f&platform.FrameKey != 0 || (!static && !minor):
		*r = refiner{repeat: r.repeat, next: now.Add(refineIdle)}
		return true
	case !static:
		r.pending = false
		if r.repeat {
			// Repeats converge on the changed area and stop when small; a
			// caret blinking forever wins back only the last few each time.
			r.pass = min(r.pass, refineRepeatMax-refineRepeatRedo)
		} else {
			r.pass = min(r.pass, len(refineQPs)-1)
		}
		if quiet := now.Add(refineIdle); quiet.After(r.next) {
			r.next = quiet
		}
	default:
		r.pending = false
		if r.repeat && size < refineSettled {
			r.pass = refineRepeatMax
		}
		gap := time.Duration(float64(size*8) / (refineShare * float64(max(kbps, 1)*1000)) * float64(time.Second))
		r.next = now.Add(max(refineMinGap, gap))
	}
	return false
}

// useRepeats switches to repeat passes and starts them now: the encoder
// refused a bounded quantizer.
func (r *refiner) useRepeats(now time.Time) {
	*r = refiner{repeat: true, next: now}
}

// due returns the quantizer for the next pass (0: a repeat pass) when one
// should be sent now.
func (r *refiner) due(now time.Time) (int, bool) {
	if r.pending && now.Sub(r.sentAt) >= refineLost {
		r.pending = false
	}
	passes := len(refineQPs)
	if r.repeat {
		passes = refineRepeatMax
	}
	if r.pending || r.next.IsZero() || now.Before(r.next) || r.pass >= passes || r.sent >= refineCap(r.repeat) {
		return 0, false
	}
	qp := 0
	if !r.repeat {
		qp = refineQPs[r.pass]
	}
	r.pass++
	r.sent++
	r.pending, r.await, r.sentAt = true, true, now
	return qp, true
}
