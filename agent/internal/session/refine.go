package session

import (
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Static-screen refinement: once nothing on the screen has changed for
// refineIdle, the encoder re-encodes the last frame in passes of falling
// quantizer until the text is sharp (Chrome Remote Desktop's approach). Each
// pass is paced so refinement takes at most refineShare of the target bitrate.

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
)

type refiner struct {
	pass    int // next index into refineQPs
	pending bool
	sentAt  time.Time
	next    time.Time // earliest next pass; zero until the first frame
}

// onFrame records an encoded frame of size bytes and reports whether
// refinement starts over: on new content, or a keyframe, which the rate
// control encodes coarsely again. A small change waits for a quiet screen
// (never sooner than the pacing allows) and redoes at most the finest pass.
func (r *refiner) onFrame(now time.Time, size int, f platform.FrameFlags, kbps int) bool {
	static := f&platform.FrameStatic != 0
	minor := f&platform.FrameMinor != 0 || size < refineSmall
	switch {
	case f&platform.FrameKey != 0 || (!static && !minor):
		*r = refiner{next: now.Add(refineIdle)}
		return true
	case !static:
		r.pass, r.pending = min(r.pass, len(refineQPs)-1), false
		if quiet := now.Add(refineIdle); quiet.After(r.next) {
			r.next = quiet
		}
	default:
		r.pending = false
		gap := time.Duration(float64(size*8) / (refineShare * float64(max(kbps, 1)*1000)) * float64(time.Second))
		r.next = now.Add(max(refineMinGap, gap))
	}
	return false
}

// due returns the quantizer for the next pass when one should be sent now.
func (r *refiner) due(now time.Time) (int, bool) {
	if r.pending && now.Sub(r.sentAt) >= refineLost {
		r.pending = false
	}
	if r.pending || r.next.IsZero() || now.Before(r.next) || r.pass >= len(refineQPs) {
		return 0, false
	}
	qp := refineQPs[r.pass]
	r.pass++
	r.pending, r.sentAt = true, now
	return qp, true
}
