package session

import (
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/ruolez/remotectl/proto"
)

var t0 = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestAutoLadder(t *testing.T) {
	cases := []struct {
		name   string
		fps    int
		res    string
		nw, nh uint32
		want   []Rung
	}{
		{"1080p60 drops fps then resolution", 60, proto.ResNative, 1920, 1080, []Rung{
			{60, proto.ResNative, 1920, 1080, 3732}, {30, proto.ResNative, 1920, 1080, 1866},
			{15, proto.ResNative, 1920, 1080, 933}, {15, proto.Res720p, 1280, 720, 414},
			{10, proto.Res720p, 1280, 720, 276}, {5, proto.Res720p, 1280, 720, 138},
		}},
		{"5K60 walks every resolution", 60, proto.ResNative, 5120, 2880, []Rung{
			{60, proto.ResNative, 5120, 2880, 26542}, {30, proto.ResNative, 5120, 2880, 13271},
			{15, proto.ResNative, 5120, 2880, 6635}, {15, proto.Res1440p, 2560, 1440, 1658},
			{15, proto.Res1080p, 1920, 1080, 933}, {15, proto.Res720p, 1280, 720, 414},
			{10, proto.Res720p, 1280, 720, 276}, {5, proto.Res720p, 1280, 720, 138},
		}},
		{"ceilings bound the top rung", 30, proto.Res1080p, 3840, 2160, []Rung{
			{30, proto.Res1080p, 1920, 1080, 1866}, {15, proto.Res1080p, 1920, 1080, 933},
			{15, proto.Res720p, 1280, 720, 414}, {10, proto.Res720p, 1280, 720, 276}, {5, proto.Res720p, 1280, 720, 138},
		}},
		{"24 fps cap", 24, proto.Res720p, 1920, 1080, []Rung{
			{24, proto.Res720p, 1280, 720, 663}, {15, proto.Res720p, 1280, 720, 414},
			{10, proto.Res720p, 1280, 720, 276}, {5, proto.Res720p, 1280, 720, 138},
		}},
		{"5 fps cap only lowers resolution", 5, proto.ResNative, 1920, 1080, []Rung{
			{5, proto.ResNative, 1920, 1080, 311}, {5, proto.Res720p, 1280, 720, 138},
		}},
		{"small screen", 60, proto.ResNative, 1024, 768, []Rung{
			{60, proto.ResNative, 1024, 768, 1415}, {30, proto.ResNative, 1024, 768, 707},
			{15, proto.ResNative, 1024, 768, 353}, {15, proto.Res720p, 960, 720, 311},
			{10, proto.Res720p, 960, 720, 207}, {5, proto.Res720p, 960, 720, 103},
		}},
	}
	for _, c := range cases {
		if got := AutoLadder(c.fps, c.res, c.nw, c.nh, 0, 0); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

type autoStep struct {
	at     int // seconds after t0
	rung   int
	reason string
}

// runAuto feeds one sample per second for seconds 1..n and records changes
// of rung.
func runAuto(st AutoState, ladder []Rung, n int, sample func(sec int) AutoSample) (AutoState, []autoStep) {
	var steps []autoStep
	for sec := 1; sec <= n; sec++ {
		prev := st.Rung
		st = NextAuto(st, t0.Add(time.Duration(sec)*time.Second), sample(sec), ladder)
		if st.Rung != prev {
			steps = append(steps, autoStep{sec, st.Rung, st.Reason})
		}
	}
	return st, steps
}

var (
	busySample      = AutoSample{SendKbps: 4000, TargetKbps: 4000} // feeds 1080p60 (3732)
	overuseSample   = AutoSample{SendKbps: 3000, TargetKbps: 3000, Overuse: true}
	lossSample      = AutoSample{SendKbps: 3000, TargetKbps: 3000, Loss: 0.12}
	healthySample   = AutoSample{SendKbps: 3000, TargetKbps: 6000} // 6000 ≥ 1.3 × 3732
	idleSample      = AutoSample{SendKbps: 40, TargetKbps: 3000}
	idleLowEstimate = AutoSample{SendKbps: 30, TargetKbps: 1000, Loss: 0.2, Overuse: true}
)

func ladder1080p() []Rung { return AutoLadder(60, proto.ResNative, 1920, 1080, 0, 0) }

func TestAutoIdleScreenNeverStepsDown(t *testing.T) {
	st, steps := runAuto(NewAutoState(t0), ladder1080p(), 600, func(int) AutoSample { return idleLowEstimate })
	if len(steps) != 0 || st.Rung != 0 {
		t.Fatalf("idle screen changed rungs: %+v", steps)
	}
}

func TestAutoCongestionStepsDownSpaced(t *testing.T) {
	_, steps := runAuto(NewAutoState(t0), ladder1080p(), 60, func(int) AutoSample { return overuseSample })
	want := []autoStep{
		{5, 1, AutoCongestion}, {10, 2, AutoCongestion}, {15, 3, AutoCongestion}, // 720p: 15 s after start
		{20, 4, AutoCongestion}, {25, 5, AutoCongestion},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %+v, want %+v", steps, want)
	}
}

func TestAutoLossStepsDown(t *testing.T) {
	_, steps := runAuto(NewAutoState(t0), ladder1080p(), 6, func(int) AutoSample { return lossSample })
	if want := []autoStep{{5, 1, AutoLoss}}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %+v, want %+v", steps, want)
	}
}

func TestAutoSingleOveruseIsNotCongestion(t *testing.T) {
	_, steps := runAuto(NewAutoState(t0), ladder1080p(), 60, func(sec int) AutoSample {
		if sec%3 == 0 {
			return overuseSample
		}
		return busySample
	})
	if len(steps) != 0 {
		t.Fatalf("one overuse in three samples stepped down: %+v", steps)
	}
}

func TestAutoWarmupDelaysStarvation(t *testing.T) {
	starved := AutoSample{SendKbps: 1800, TargetKbps: 2000} // below 1080p60's 3732, encoder using it
	st := NewAutoState(t0)
	st = NextAuto(st, t0.Add(time.Second), starved, ladder1080p())
	if st.Reason != AutoWarmup {
		t.Fatalf("reason during warm-up = %q", st.Reason)
	}
	_, steps := runAuto(NewAutoState(t0), ladder1080p(), 40, func(int) AutoSample { return starved })
	// Warm-up ends at 10 s; three starved samples (10, 11, 12) step down once,
	// and 2000 kbps feeds 1080p30 (1866) so it stays there.
	if want := []autoStep{{12, 1, AutoStarved}}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %+v, want %+v", steps, want)
	}
}

func TestAutoStarvationNeedsTheEncoderUsingTheTarget(t *testing.T) {
	light := AutoSample{SendKbps: 1200, TargetKbps: 2000} // below MinKbps but only 60% used
	if _, steps := runAuto(NewAutoState(t0), ladder1080p(), 60, func(int) AutoSample { return light }); len(steps) != 0 {
		t.Fatalf("light use stepped down: %+v", steps)
	}
}

func TestAutoHoldDoublesOnBounceAndResetsWhenCalm(t *testing.T) {
	phases := []struct {
		from int
		s    AutoSample
	}{{0, overuseSample}, {6, healthySample}, {16, overuseSample}, {21, healthySample}, {41, overuseSample},
		{46, healthySample}, {200, overuseSample}, {203, healthySample}}
	st, steps := runAuto(NewAutoState(t0), ladder1080p(), 230, func(sec int) AutoSample {
		s := phases[0].s
		for _, p := range phases {
			if sec >= p.from {
				s = p.s
			}
		}
		return s
	})
	want := []autoStep{
		{5, 1, AutoCongestion}, {15, 0, AutoSteady}, // hold 10 s
		{20, 1, AutoCongestion}, {40, 0, AutoSteady}, // bounce: hold 20 s
		{45, 1, AutoCongestion}, {85, 0, AutoSteady}, // bounce: hold 40 s
		{201, 1, AutoCongestion}, {211, 0, AutoSteady}, // calm for 120 s: hold back to 10 s
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps =\n%+v\nwant\n%+v", steps, want)
	}
	if st.UpHold != 10*time.Second {
		t.Fatalf("UpHold = %v", st.UpHold)
	}
}

func TestAutoStepUpNeedsHeadroom(t *testing.T) {
	st := NewAutoState(t0)
	st.Rung = 1
	short := AutoSample{SendKbps: 3000, TargetKbps: 4800} // < 1.3 × 3732
	if _, steps := runAuto(st, ladder1080p(), 60, func(int) AutoSample { return short }); len(steps) != 0 {
		t.Fatalf("stepped up without headroom: %+v", steps)
	}
}

func TestAutoIdleProbesUpward(t *testing.T) {
	st := NewAutoState(t0)
	st.Rung = 2
	_, steps := runAuto(st, ladder1080p(), 70, func(int) AutoSample { return idleSample })
	if want := []autoStep{{30, 1, AutoProbe}, {60, 0, AutoProbe}}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %+v, want %+v", steps, want)
	}
}

func TestAutoResolutionChangesAtMostEvery15s(t *testing.T) {
	ladder := AutoLadder(15, proto.ResNative, 5120, 2880, 0, 0) // every step down is a resolution change until 720p
	st, steps := runAuto(NewAutoState(t0), ladder, 70, func(int) AutoSample { return overuseSample })
	want := []autoStep{{15, 1, AutoCongestion}, {30, 2, AutoCongestion}, {45, 3, AutoCongestion},
		{50, 4, AutoCongestion}, {55, 5, AutoCongestion}}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %+v, want %+v", steps, want)
	}
	// A resolution change restarts capture, which starts a new warm-up.
	if !st.Since.Equal(t0.Add(45 * time.Second)) {
		t.Fatalf("Since = %v", st.Since)
	}
}

func TestAutoClampsAStaleRung(t *testing.T) {
	st := NewAutoState(t0)
	st.Rung = 9
	if st = NextAuto(st, t0.Add(time.Second), busySample, ladder1080p()); st.Rung != 5 {
		t.Fatalf("rung = %d", st.Rung)
	}
}

// sampleFrom derives a sample from random seeds; idle forces SendKbps below
// half the target.
func sampleFrom(seed uint32, idle bool) AutoSample {
	r := rand.New(rand.NewSource(int64(seed)))
	target := 1000 + r.Intn(19000)
	send := r.Intn(target * 2)
	if idle {
		send = r.Intn(target / 2)
	}
	return AutoSample{SendKbps: send, TargetKbps: target, Loss: r.Float64() * 0.3, Overuse: r.Intn(2) == 0}
}

func TestAutoInvariants(t *testing.T) {
	ladder := AutoLadder(60, proto.ResNative, 5120, 2880, 0, 0)
	cfg := &quick.Config{MaxCount: 300}

	idleNeverDown := func(start uint8, seeds []uint32) bool {
		st := NewAutoState(t0)
		st.Rung = int(start) % len(ladder)
		for i, s := range seeds {
			prev := st.Rung
			st = NextAuto(st, t0.Add(time.Duration(i+1)*time.Second), sampleFrom(s, true), ladder)
			if st.Rung > prev {
				return false
			}
		}
		return true
	}
	if err := quick.Check(idleNeverDown, cfg); err != nil {
		t.Errorf("idle samples stepped down: %v", err)
	}

	spaced := func(start uint8, seeds []uint32) bool {
		st := NewAutoState(t0)
		st.Rung = int(start) % len(ladder)
		var lastChange, lastRes time.Time = t0, t0
		for i, s := range seeds {
			now := t0.Add(time.Duration(i+1) * time.Second)
			prev := st.Rung
			st = NextAuto(st, now, sampleFrom(s, false), ladder)
			if st.Rung < 0 || st.Rung >= len(ladder) {
				return false
			}
			if st.Rung == prev {
				continue
			}
			if d := st.Rung - prev; d != 1 && d != -1 {
				return false
			}
			if now.Sub(lastChange) < 5*time.Second {
				return false
			}
			lastChange = now
			a, b := ladder[prev], ladder[st.Rung]
			if a.W != b.W || a.H != b.H {
				if now.Sub(lastRes) < 15*time.Second {
					return false
				}
				lastRes = now
			}
		}
		return true
	}
	if err := quick.Check(spaced, cfg); err != nil {
		t.Errorf("changes not spaced: %v", err)
	}
}
