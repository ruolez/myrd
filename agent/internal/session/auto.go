package session

import (
	"math/bits"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// Auto preset: a ladder of (fps, size) rungs walked by a pure state machine
// fed one sample per second. Frame rate goes first, then resolution.

const (
	AutoSteady     = "steady"
	AutoWarmup     = "warmup"
	AutoCongestion = "congestion"
	AutoLoss       = "loss"
	AutoStarved    = "starved"
	AutoProbe      = "probe"
)

const (
	autoWarmup     = 10 * time.Second // after a start, restart or settings change: no starvation
	autoMinGap     = 5 * time.Second
	autoResGap     = 15 * time.Second // resolution changes restart capture
	autoUpHoldMin  = 10 * time.Second
	autoUpHoldMax  = 160 * time.Second
	autoCalm       = 120 * time.Second // without a step down: hold back to the minimum
	autoBounce     = 30 * time.Second  // a step down this soon after a step up doubles the hold
	autoStarvedN   = 3
	autoIdleProbeN = 30
	autoMaxLoss    = 0.05
	autoHeadroom   = 1.3
)

// Rung is one ladder step. MinKbps ≈ 0.03 bit per pixel per frame.
type Rung struct {
	FPS     int
	Res     string
	W, H    uint32
	MinKbps int
}

func newRung(fps int, res string, w, h uint32) Rung {
	return Rung{fps, res, w, h, int(int64(w) * int64(h) * int64(fps) * 3 / 100000)}
}

// AutoLadder: fps 60→30→15 at the resolution ceiling, then 1440p→1080p→720p
// at 15 fps (sizes that are not smaller are skipped), then 10 and 5 fps.
func AutoLadder(fpsCap int, resCap string, nw, nh uint32, maxW, maxH int) []Rung {
	w, h := OutputSize(nw, nh, resCap, maxW, maxH)
	ladder := []Rung{newRung(fpsCap, resCap, w, h)}
	for _, f := range []int{30, 15} {
		if f < fpsCap {
			ladder = append(ladder, newRung(f, resCap, w, h))
		}
	}
	fps, res := min(15, fpsCap), resCap
	for _, r := range []string{proto.Res1440p, proto.Res1080p, proto.Res720p} {
		rw, rh := OutputSize(nw, nh, r, maxW, maxH)
		if uint64(rw)*uint64(rh) < uint64(w)*uint64(h) {
			w, h, res = rw, rh, r
			ladder = append(ladder, newRung(fps, res, w, h))
		}
	}
	for _, f := range []int{10, 5} {
		if f < fps {
			ladder = append(ladder, newRung(f, res, w, h))
		}
	}
	return ladder
}

// AutoSample is one second of the session: what was sent, what the encoder
// targets, and the congestion controller's view.
type AutoSample struct {
	SendKbps   int
	TargetKbps int
	Loss       float64 // 0..1
	Overuse    bool    // GCC delay detector state
}

type AutoState struct {
	Rung          int
	Reason        string
	Since         time.Time // start of the current warm-up
	LastChange    time.Time
	LastResChange time.Time
	LastUp        time.Time
	LastDown      time.Time
	UpHold        time.Duration

	overuse uint8 // last three samples, bit 0 newest
	starved int
	idle    int
}

func NewAutoState(now time.Time) AutoState {
	return AutoState{Reason: AutoWarmup, Since: now, LastChange: now, LastResChange: now, UpHold: autoUpHoldMin}
}

func (st AutoState) canMove(now time.Time, ladder []Rung, to int) bool {
	if to < 0 || to >= len(ladder) || now.Sub(st.LastChange) < autoMinGap {
		return false
	}
	a, b := ladder[st.Rung], ladder[to]
	return (a.W == b.W && a.H == b.H) || now.Sub(st.LastResChange) >= autoResGap
}

func (st AutoState) moveTo(now time.Time, ladder []Rung, to int, reason string) AutoState {
	a, b := ladder[st.Rung], ladder[to]
	if a.W != b.W || a.H != b.H {
		st.LastResChange, st.Since = now, now
	}
	st.Rung, st.Reason, st.LastChange = to, reason, now
	st.overuse, st.starved, st.idle = 0, 0, 0
	return st
}

// NextAuto advances Auto by one sample. It steps down on congestion (loss, or
// GCC overuse in two of the last three samples) or starvation (target below
// the rung's need while the encoder really uses it), and up after UpHold when
// the estimate has headroom for the next rung, or as a probe after a long idle
// stretch. A mostly idle screen never steps down: GCC cannot estimate above
// what is actually sent, so a low estimate then says nothing about the link.
func NextAuto(st AutoState, now time.Time, s AutoSample, ladder []Rung) AutoState {
	if len(ladder) == 0 {
		return st
	}
	st.Rung = max(0, min(st.Rung, len(ladder)-1))
	if st.UpHold == 0 {
		st.UpHold = autoUpHoldMin
	}
	if !st.LastDown.IsZero() && now.Sub(st.LastDown) >= autoCalm {
		st.UpHold = autoUpHoldMin
	}
	st.overuse = (st.overuse << 1) & 0b111
	if s.Overuse {
		st.overuse |= 1
	}
	warm := now.Sub(st.Since) < autoWarmup
	busy := s.TargetKbps > 0 && s.SendKbps*2 >= s.TargetKbps
	cur := ladder[st.Rung]
	if !warm && busy && s.TargetKbps < cur.MinKbps && s.SendKbps*10 >= s.TargetKbps*7 {
		st.starved++
	} else {
		st.starved = 0
	}
	if busy {
		st.idle = 0
	} else {
		st.idle++
	}

	trouble := ""
	switch {
	case s.Loss > autoMaxLoss:
		trouble = AutoLoss
	case bits.OnesCount8(st.overuse) >= 2:
		trouble = AutoCongestion
	case st.starved >= autoStarvedN:
		trouble = AutoStarved
	}
	if trouble != "" {
		if busy && st.canMove(now, ladder, st.Rung+1) {
			if !st.LastUp.IsZero() && now.Sub(st.LastUp) < autoBounce {
				st.UpHold = min(2*st.UpHold, autoUpHoldMax)
			}
			st = st.moveTo(now, ladder, st.Rung+1, trouble)
			st.LastDown = now
			return st
		}
	} else if st.Rung > 0 && !warm && now.Sub(st.LastChange) >= st.UpHold && st.canMove(now, ladder, st.Rung-1) {
		next := ladder[st.Rung-1]
		reason := ""
		switch {
		case busy && st.overuse == 0 && float64(s.TargetKbps) >= autoHeadroom*float64(next.MinKbps):
			reason = AutoSteady
		case st.idle >= autoIdleProbeN:
			reason = AutoProbe
		}
		if reason != "" {
			st = st.moveTo(now, ladder, st.Rung-1, reason)
			st.LastUp = now
			return st
		}
	}
	if warm && (st.Reason == AutoSteady || st.Reason == "") {
		st.Reason = AutoWarmup // a restart after a step keeps that step's reason
	} else if !warm && st.Reason == AutoWarmup {
		st.Reason = AutoSteady
	}
	return st
}
