package session

import "time"

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
	// A content frame this small changed a small area (a caret, a clock):
	// skipped macroblocks keep their refined quality, so only the last
	// refineRewind passes are redone over it.
	refineSmall  = 4096
	refineRewind = 2
)

type refiner struct {
	pass    int // next index into refineQPs
	pending bool
	sentAt  time.Time
	next    time.Time // earliest next pass; zero until the first frame
}

// onFrame records an encoded frame of size bytes: new content (or a
// keyframe, which the rate control encodes coarsely again) starts over; a
// small change backs up a few passes.
func (r *refiner) onFrame(now time.Time, size int, keyframe, static bool, kbps int) {
	if keyframe || (!static && size >= refineSmall) {
		*r = refiner{next: now.Add(refineIdle)}
		return
	}
	if !static {
		r.pass, r.pending, r.next = max(0, r.pass-refineRewind), false, now.Add(refineIdle)
		return
	}
	r.pending = false
	gap := time.Duration(float64(size*8) / (refineShare * float64(max(kbps, 1)*1000)) * float64(time.Second))
	r.next = now.Add(max(refineMinGap, gap))
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
