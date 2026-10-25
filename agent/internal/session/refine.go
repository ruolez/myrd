package session

import (
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Static-screen refinement: once nothing on the screen has changed for
// refineIdle, the encoder re-encodes the last frame in passes of falling
// quantizer until the text is sharp (Chrome Remote Desktop's approach), then
// at the finest one until a pass comes back small: below ~8 Mbps the rate
// control caps each pass, so one finest pass leaves the picture soft. Each
// pass is paced so refinement takes at most refineShare of the target bitrate.
// The ladder starts at 30: the frame of new content is rarely coarser, and
// passes above its quantizer change nothing (measured on x264 and
// VideoToolbox).
// An encoder that cannot bound its quantizer (some Windows hardware MFTs)
// gets repeat passes instead: the same picture again under the normal rate
// control, each one correcting what the last left, until one comes back
// small. Without them its first, coarse frame of a still screen stayed.
// Where the bandwidth carries it, a lossless pass (VP9 only) ends the
// refinement: the finest quantizer still leaves small text slightly soft.

var refineQPs = []int{30, 24, 21, 18}

const (
	refineIdle   = 200 * time.Millisecond
	refineMinGap = 50 * time.Millisecond
	refineLost   = time.Second // a pass that never came back: new content won
	refineShare  = 0.5
	refineTick   = 25 * time.Millisecond
	// A change to a small area (a caret, a clock: FrameMinor, or a content
	// frame under refineSmall from a backend that cannot tell) leaves the
	// rest of the screen refined: the passes still to come cover it, and a
	// finished refinement sends its finest pass again.
	refineSmall = 4096

	refineFinestMax  = 8    // passes at the finest quantizer at most
	refineRepeatMax  = 24   // repeat passes at most
	refineRepeatRedo = 3    // passes at the finest quantizer (or repeats) a small change can win back
	refineSettled    = 1500 // a pass this small changed next to nothing: converged
	// A finest pass under one byte per refineSettledPx pixels (2 KB at 1080p)
	// has converged too: larger screens leave more noise in a still picture.
	refineSettledPx = 1000

	// A lossless pass costs about refineLosslessRatio times the first pass at
	// the finest quantizer (65-78x on text, a page with a photo and a full
	// screen photo at 1080p: 360 KB to 1.3 MB) and is sent only when that
	// goes out within refineLosslessSend at the estimated bandwidth.
	refineLosslessRatio = 80
	refineLosslessSend  = 150 * time.Millisecond
)

// refinePasses is the length of a refinement: the ladder with its finest
// quantizer up to refineFinestMax times, or refineRepeatMax repeats.
func refinePasses(repeat bool) int {
	if repeat {
		return refineRepeatMax
	}
	return len(refineQPs) - 1 + refineFinestMax
}

// refineCap bounds the passes sent since the last real content frame (new
// content or a keyframe, neither a pass's output): a full refinement plus as
// many again for the passes small changes redo. Whatever the flags an encoder
// reports, a still screen then stops costing frames.
func refineCap(repeat bool) int {
	return 2 * refinePasses(repeat)
}

type refiner struct {
	pass    int  // passes sent: index into refineQPs (the last one repeats), or repeats
	px      int  // pixels of the encoded picture (0 = unknown), for the converged size
	repeat  bool // repeat passes (quantizer 0) instead of refineQPs
	pending bool // a pass is out and may still come back (until refineLost)
	await   bool // a pass was requested and no frame has arrived since
	sent    int  // passes since the last real content frame (refineCap)
	sentAt  time.Time
	next    time.Time // earliest next pass; zero until the first frame

	// losslessKbps is the bandwidth estimate a lossless pass may use, 0 when
	// the encoder or the stream settings have none.
	losslessKbps int
	finest       int  // size of the first pass at the finest quantizer since new content
	exact        bool // the lossless pass was sent; a small change redoes it
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
		*r = refiner{repeat: r.repeat, px: r.px, losslessKbps: r.losslessKbps, next: now.Add(refineIdle)}
		return true
	case !static:
		r.pending, r.exact = false, false
		// The finest passes (or repeats) converge on the changed area and
		// stop when small; a caret blinking forever wins back only the last
		// few each time. Coarser passes still to come cover it anyway.
		r.pass = min(r.pass, refinePasses(r.repeat)-refineRepeatRedo)
		if quiet := now.Add(refineIdle); quiet.After(r.next) {
			r.next = quiet
		}
	default:
		r.pending = false
		finest, settled := r.repeat, refineSettled
		if !r.repeat {
			finest, settled = r.pass >= len(refineQPs), max(refineSettled, r.px/refineSettledPx)
			if r.pass == len(refineQPs) && r.finest == 0 {
				r.finest = size
			}
		}
		if finest && size < settled {
			r.pass = refinePasses(r.repeat)
		}
		gap := time.Duration(float64(size*8) / (refineShare * float64(max(kbps, 1)*1000)) * float64(time.Second))
		r.next = now.Add(max(refineMinGap, gap))
	}
	return false
}

// useRepeats switches to repeat passes and starts them now: the encoder
// refused a bounded quantizer.
func (r *refiner) useRepeats(now time.Time) {
	*r = refiner{repeat: true, px: r.px, next: now}
}

// losslessFits reports whether the predicted lossless pass goes out within
// refineLosslessSend at losslessKbps.
func (r *refiner) losslessFits() bool {
	bits := float64(refineLosslessRatio * r.finest * 8)
	return !r.repeat && r.finest > 0 && r.losslessKbps > 0 &&
		bits/float64(r.losslessKbps*1000) <= refineLosslessSend.Seconds()
}

// due returns the quantizer for the next pass (0: a repeat pass,
// platform.RefineLossless: the lossless one) when one should be sent now.
func (r *refiner) due(now time.Time) (int, bool) {
	if r.pending && now.Sub(r.sentAt) >= refineLost {
		r.pending = false
	}
	if r.pending || r.next.IsZero() || now.Before(r.next) || r.sent >= refineCap(r.repeat) {
		return 0, false
	}
	var qp int
	switch {
	case r.pass < refinePasses(r.repeat):
		if !r.repeat {
			qp = refineQPs[min(r.pass, len(refineQPs)-1)]
		}
		r.pass++
	case !r.exact && r.losslessFits():
		qp, r.exact = platform.RefineLossless, true
	default:
		return 0, false
	}
	r.sent++
	r.pending, r.await, r.sentAt = true, true, now
	return qp, true
}
