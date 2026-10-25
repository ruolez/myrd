package session

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

func TestRefinerWaitsForAQuietScreen(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	if _, ok := r.due(t0); ok {
		t.Fatal("refined before any frame was encoded")
	}
	r.onFrame(t0, 50_000, 0, 4000)
	if _, ok := r.due(t0.Add(refineIdle - time.Millisecond)); ok {
		t.Fatal("refined while the screen was still changing")
	}
	if qp, ok := r.due(t0.Add(refineIdle)); !ok || qp != refineQPs[0] {
		t.Fatalf("due = %d, %v; want the first pass (%d)", qp, ok, refineQPs[0])
	}
}

// A static screen walks the ladder, then repeats the finest pass until one
// comes back small; each pass is sent after the previous one came back and
// its bits had time to drain at refineShare of the target.
func TestRefinerPacesPassesToTheBitrate(t *testing.T) {
	const kbps = 4000 // 2 Mbps for refinement: 25 KB takes 100 ms, 100 KB takes 400 ms
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, platform.FrameKey, kbps)
	sizes := []int{1_000, 25_000, 100_000, 50_000, 10_000, refineSettled - 1}
	wantGap := []time.Duration{refineMinGap, 100 * time.Millisecond, 400 * time.Millisecond, 200 * time.Millisecond,
		refineMinGap}
	finest := refineQPs[len(refineQPs)-1]
	wantQPs := append(slices.Clone(refineQPs), finest, finest)
	now := t0.Add(refineIdle)
	var qps []int
	for i, size := range sizes {
		qp, ok := r.due(now)
		if !ok {
			t.Fatalf("pass %d not due at +%v", i, now.Sub(t0))
		}
		qps = append(qps, qp)
		if _, again := r.due(now.Add(time.Millisecond)); again {
			t.Fatalf("pass %d: a second pass before the first came back", i)
		}
		now = now.Add(20 * time.Millisecond) // the encoded pass arrives
		r.onFrame(now, size, platform.FrameStatic, kbps)
		if i < len(wantGap) {
			if _, early := r.due(now.Add(wantGap[i] - time.Millisecond)); early {
				t.Fatalf("pass %d after a %d-byte pass: due before %v", i+1, size, wantGap[i])
			}
			now = now.Add(wantGap[i])
		}
	}
	if !slices.Equal(qps, wantQPs) {
		t.Fatalf("passes = %v, want %v", qps, wantQPs)
	}
	if _, ok := r.due(now.Add(time.Hour)); ok {
		t.Fatal("refined after a finest pass came back small")
	}
}

// Below ~8 Mbps the rate control caps each pass, so the finest one repeats
// until a pass changes next to nothing: under refineSettled bytes, or one per
// refineSettledPx pixels on a larger picture.
func TestRefinerFinestPassRepeatsUntilItConverges(t *testing.T) {
	for _, c := range []struct {
		name    string
		px      int
		settled int // smallest pass size that has not converged
	}{
		{"size unknown", 0, refineSettled},
		{"720p", 1280 * 720, refineSettled},
		{"1080p", 1920 * 1080, 1920 * 1080 / refineSettledPx},
		{"4K", 3840 * 2160, 3840 * 2160 / refineSettledPx},
	} {
		t0 := time.Unix(1000, 0)
		r := refiner{px: c.px}
		r.onFrame(t0, 50_000, 0, 4000)
		var qps []int
		n := refineRun(&r, t0, time.Minute, func(i int) (int, platform.FrameFlags) {
			switch {
			case i < len(refineQPs):
				return 15_000, platform.FrameStatic
			case i == len(refineQPs):
				return c.settled, platform.FrameStatic
			}
			return c.settled - 1, platform.FrameStatic
		}, nil, &qps)
		finest := refineQPs[len(refineQPs)-1]
		want := append(slices.Clone(refineQPs), finest, finest)
		if n != len(want) || !slices.Equal(qps, want) {
			t.Fatalf("%s: passes %v, want %v", c.name, qps, want)
		}
	}
}

func TestRefinerStartsOverOnNewContentOrAKeyframe(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags platform.FrameFlags
	}{
		{"new content", 0},
		{"keyframe (PLI) while static", platform.FrameKey | platform.FrameStatic},
	} {
		t0 := time.Unix(1000, 0)
		var r refiner
		r.onFrame(t0, 50_000, 0, 4000)
		now := t0.Add(refineIdle)
		r.due(now)
		r.onFrame(now, 1000, platform.FrameStatic, 4000)
		now = now.Add(refineMinGap)
		r.due(now)
		r.onFrame(now, 1000, platform.FrameStatic, 4000)
		now = now.Add(refineMinGap)
		r.onFrame(now, 50_000, c.flags, 4000)
		if _, ok := r.due(now.Add(refineIdle - time.Millisecond)); ok {
			t.Fatalf("%s: refined before the screen was quiet again", c.name)
		}
		if qp, _ := r.due(now.Add(refineIdle)); qp != refineQPs[0] {
			t.Fatalf("%s: next pass qp %d, want the coarsest (%d)", c.name, qp, refineQPs[0])
		}
	}
}

// A caret blink or a clock tick touches a few macroblocks: the passes still
// to come cover them, so refinement carries on once the screen is quiet.
func TestRefinerCarriesOnAfterASmallChange(t *testing.T) {
	for _, c := range []struct {
		name  string
		size  int
		flags platform.FrameFlags
	}{
		{"small frame", refineSmall - 1, 0},
		{"large frame of a minor change", 60_000, platform.FrameMinor},
	} {
		t0 := time.Unix(1000, 0)
		var r refiner
		r.onFrame(t0, 50_000, 0, 4000)
		now := t0.Add(refineIdle)
		for range 2 { // passes 30, 24
			r.due(now)
			r.onFrame(now, 500, platform.FrameStatic, 4000)
			now = now.Add(refineMinGap)
		}
		if r.onFrame(now, c.size, c.flags, 4000) {
			t.Fatalf("%s: refinement started over", c.name)
		}
		if _, ok := r.due(now.Add(refineIdle - time.Millisecond)); ok {
			t.Fatalf("%s: refined before the screen was quiet again", c.name)
		}
		if qp, _ := r.due(now.Add(refineIdle)); qp != refineQPs[2] {
			t.Fatalf("%s: qp %d, want %d (the next pass)", c.name, qp, refineQPs[2])
		}
	}
}

func TestRefinerRedoesTheFinestPassAfterFinishing(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, 0, 4000)
	now := t0.Add(refineIdle)
	for range refineQPs {
		r.due(now)
		r.onFrame(now, 500, platform.FrameStatic, 4000)
		now = now.Add(refineMinGap)
	}
	if _, ok := r.due(now.Add(time.Hour)); ok {
		t.Fatal("refined past the finest pass")
	}
	r.onFrame(now, 800, platform.FrameMinor, 4000)
	finest := refineQPs[len(refineQPs)-1]
	if qp, ok := r.due(now.Add(refineIdle)); !ok || qp != finest {
		t.Fatalf("after a caret blink: %d, %v; want the finest pass (%d) again", qp, ok, finest)
	}
	r.onFrame(now.Add(refineIdle), 300, platform.FrameStatic, 4000)
	if _, ok := r.due(now.Add(time.Hour)); ok {
		t.Fatal("refined again without a change")
	}
}

func TestRefinerRetriesAPassThatNeverCameBack(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, 0, 4000)
	now := t0.Add(refineIdle)
	first, _ := r.due(now)
	if _, ok := r.due(now.Add(refineLost - time.Millisecond)); ok {
		t.Fatal("next pass while the previous one may still arrive")
	}
	if qp, ok := r.due(now.Add(refineLost)); !ok || qp != refineQPs[1] {
		t.Fatalf("after a lost pass (%d): due = %d, %v; want %d", first, qp, ok, refineQPs[1])
	}
}

// A busy desktop (a blinking caret, a clock) sends minor changes all the
// time, large frames when the encoder spends spare bits: refinement must
// still reach its finest pass.
func TestRefinerConvergesUnderFrequentMinorChanges(t *testing.T) {
	const kbps = 4000
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, 0, kbps)
	now := t0.Add(refineIdle)
	var finest int
	for step := 0; step < 400 && finest != refineQPs[len(refineQPs)-1]; step++ {
		if qp, ok := r.due(now); ok {
			now = now.Add(20 * time.Millisecond)
			r.onFrame(now, 100_000, platform.FrameStatic, kbps) // every pass is expensive: 400 ms of budget
			finest = qp
		}
		if step%5 == 0 { // a 30 KB minor change every 250 ms
			r.onFrame(now, 30_000, platform.FrameMinor, kbps)
		}
		now = now.Add(50 * time.Millisecond)
	}
	if finest != refineQPs[len(refineQPs)-1] {
		t.Fatalf("finest pass %d after %v of minor changes, want %d", finest, now.Sub(t0), refineQPs[len(refineQPs)-1])
	}
}

func TestRefinerMinorChangeKeepsThePacing(t *testing.T) {
	const kbps = 4000
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, 0, kbps)
	now := t0.Add(refineIdle)
	r.due(now)
	r.onFrame(now, 200_000, platform.FrameStatic, kbps) // 800 ms of budget
	r.onFrame(now.Add(100*time.Millisecond), 30_000, platform.FrameMinor, kbps)
	if _, ok := r.due(now.Add(800*time.Millisecond - time.Millisecond)); ok {
		t.Fatal("a minor change cut the refinement pacing short")
	}
	if qp, ok := r.due(now.Add(800 * time.Millisecond)); !ok || qp != refineQPs[1] {
		t.Fatalf("after the gap: %d, %v; want the next pass (%d)", qp, ok, refineQPs[1])
	}
}

// An encoder that cannot bound its quantizer gets repeat passes, each paced
// to its size, until one comes back small: the picture has converged.
func TestRefinerRepeatsUntilThePictureSettles(t *testing.T) {
	const kbps = 4000 // 2 Mbps for refinement: 50 KB takes 200 ms
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 80_000, platform.FrameKey, kbps)
	now := t0.Add(refineIdle)
	if qp, ok := r.due(now); !ok || qp != refineQPs[0] {
		t.Fatalf("first pass %d, %v", qp, ok)
	}
	r.useRepeats(now) // the encoder refused it
	sizes := []int{50_000, 30_000, 8_000, refineSettled - 1}
	for i, size := range sizes {
		qp, ok := r.due(now)
		if !ok || qp != 0 {
			t.Fatalf("repeat %d: %d, %v; want a repeat pass (0)", i, qp, ok)
		}
		r.onFrame(now, size, platform.FrameStatic, kbps)
		gap := max(refineMinGap, time.Duration(size*8)*time.Second/(kbps*1000/2))
		if _, early := r.due(now.Add(gap - time.Millisecond)); early {
			t.Fatalf("repeat %d after %d bytes: due before %v", i+1, size, gap)
		}
		now = now.Add(gap)
	}
	if _, ok := r.due(now.Add(time.Hour)); ok {
		t.Fatal("repeated after a pass came back small")
	}
	// New content starts the repeats over, still without a quantizer.
	r.onFrame(now, 80_000, 0, kbps)
	if qp, ok := r.due(now.Add(refineIdle)); !ok || qp != 0 {
		t.Fatalf("after new content: %d, %v; want a repeat pass", qp, ok)
	}
}

func TestRefinerRepeatsStopAtTheLimit(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.useRepeats(t0)
	now, n := t0, 0
	for ; n < 100; n++ {
		if _, ok := r.due(now); !ok {
			break
		}
		r.onFrame(now, 20_000, platform.FrameStatic, 40_000) // never converges
		now = now.Add(time.Second)
	}
	if n != refineRepeatMax {
		t.Fatalf("%d repeat passes on a picture that never settles, want %d", n, refineRepeatMax)
	}
}

// refineRun drives a refiner for d: a pass's output comes back 20 ms after
// it was requested with the flags and size out(i) gives the i-th pass, and
// change, when set, reports a frame of screen content at that moment. It
// returns the passes sent and appends their quantizers to qps.
func refineRun(r *refiner, start time.Time, d time.Duration, out func(i int) (int, platform.FrameFlags),
	change func(at time.Duration) (int, platform.FrameFlags, bool), qps ...*[]int) int {
	const kbps = 4000
	n := 0
	var back time.Time // when the pass in flight comes back; zero = none
	for at := time.Duration(0); at < d; at += refineTick {
		now := start.Add(at)
		if !back.IsZero() && !now.Before(back) {
			size, f := out(n - 1)
			r.onFrame(now, size, f, kbps)
			back = time.Time{}
		}
		if change != nil {
			if size, f, ok := change(at); ok {
				r.onFrame(now, size, f, kbps)
			}
		}
		if qp, ok := r.due(now); ok {
			n++
			back = now.Add(20 * time.Millisecond)
			for _, q := range qps {
				*q = append(*q, qp)
			}
		}
	}
	return n
}

func TestRefinerStillScreenFinishesAndStops(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t0 := time.Unix(1000, 0)
		var r refiner
		if repeat {
			r.useRepeats(t0)
		} else {
			r.onFrame(t0, 50_000, platform.FrameKey, 4000)
		}
		// A picture that never converges: every pass is mid-sized.
		n := refineRun(&r, t0, 10*time.Minute, func(int) (int, platform.FrameFlags) { return 20_000, platform.FrameStatic }, nil)
		if want := refinePasses(repeat); n != want {
			t.Fatalf("repeat=%v: %d passes on a still screen, want %d", repeat, n, want)
		}
	}
}

// A caret blinking every 530 ms is a minor change each time; in repeat mode
// (an encoder without a quantizer cap) it used to drop the passes back to 0,
// keeping ~6 extra frames a second flowing for as long as the caret blinked.
func TestRefinerBlinkingCaretSettlesInRepeatMode(t *testing.T) {
	const blink = 530 * time.Millisecond
	caret := func(at time.Duration) (int, platform.FrameFlags, bool) {
		return 900, platform.FrameMinor, at > 0 && at%blink < refineTick
	}
	for _, c := range []struct {
		name string
		size int // of every repeat pass
	}{
		{"passes come back small", refineSettled - 1},
		{"passes never come back small", 20_000},
	} {
		t0 := time.Unix(1000, 0)
		var r refiner
		r.useRepeats(t0)
		first := refineRun(&r, t0, time.Minute, func(int) (int, platform.FrameFlags) { return c.size, platform.FrameStatic }, caret)
		if first > refineCap(true) {
			t.Fatalf("%s: %d passes in the first minute, cap %d", c.name, first, refineCap(true))
		}
		later := refineRun(&r, t0.Add(time.Minute), 10*time.Minute, func(int) (int, platform.FrameFlags) { return c.size, platform.FrameStatic }, caret)
		if later != 0 {
			t.Fatalf("%s: %d more passes over 10 minutes of a blinking caret (after %d)", c.name, later, first)
		}
	}
}

// Real content restores the budget the cap took away.
func TestRefinerCapResetsOnNewContent(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.useRepeats(t0)
	caret := func(at time.Duration) (int, platform.FrameFlags, bool) {
		return 900, platform.FrameMinor, at%(530*time.Millisecond) < refineTick
	}
	pass := func(int) (int, platform.FrameFlags) { return 20_000, platform.FrameStatic }
	refineRun(&r, t0, 5*time.Minute, pass, caret)
	now := t0.Add(5 * time.Minute)
	r.onFrame(now, 80_000, 0, 4000)
	if n := refineRun(&r, now, time.Minute, pass, nil); n != refineRepeatMax {
		t.Fatalf("%d repeat passes after new content, want %d", n, refineRepeatMax)
	}
}

// Windows matches the refinement flag back by sample time: when the MFT
// alters it, the pass arrives as plain content (or as a keyframe). It must
// still count as that pass, not restart refinement.
func TestRefinerPassWithoutTheStaticFlagIsStillThePass(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags platform.FrameFlags
	}{
		{"no flags", 0},
		{"keyframe", platform.FrameKey},
	} {
		t0 := time.Unix(1000, 0)
		var r refiner
		r.onFrame(t0, 50_000, 0, 4000)
		n := refineRun(&r, t0, 10*time.Minute, func(int) (int, platform.FrameFlags) { return 20_000, c.flags }, nil)
		if want := refinePasses(false); n != want {
			t.Fatalf("%s: %d passes on a still screen, want %d", c.name, n, want)
		}
		// The same in repeat mode.
		r.useRepeats(t0)
		if n := refineRun(&r, t0, 10*time.Minute, func(int) (int, platform.FrameFlags) { return 20_000, c.flags }, nil); n != refineRepeatMax {
			t.Fatalf("%s, repeats: %d passes on a still screen, want %d", c.name, n, refineRepeatMax)
		}
	}
}

// Content that arrives after a pass came back still starts over.
func TestRefinerContentAfterAPassStartsOver(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, 0, 4000)
	now := t0.Add(refineIdle)
	r.due(now)
	r.onFrame(now.Add(20*time.Millisecond), 20_000, platform.FrameStatic, 4000)
	if !r.onFrame(now.Add(40*time.Millisecond), 50_000, 0, 4000) {
		t.Fatal("new content after the pass came back did not start over")
	}
}

// On a picture that never converges a small change wins back only the last
// few passes (finest ones, or repeats), not the whole refinement.
func TestRefinerMinorChangeRedoesOnlyTheLastPasses(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t0 := time.Unix(1000, 0)
		var r refiner
		if repeat {
			r.useRepeats(t0)
		} else {
			r.onFrame(t0, 50_000, 0, 4000)
		}
		pass := func(int) (int, platform.FrameFlags) { return 20_000, platform.FrameStatic }
		if n := refineRun(&r, t0, time.Minute, pass, nil); n != refinePasses(repeat) {
			t.Fatalf("repeat=%v: %d passes, want %d", repeat, n, refinePasses(repeat))
		}
		now := t0.Add(time.Minute)
		r.onFrame(now, 900, platform.FrameMinor, 4000)
		var qps []int
		if n := refineRun(&r, now, time.Minute, pass, nil, &qps); n != refineRepeatRedo {
			t.Fatalf("repeat=%v: %d passes after a caret blink, want %d", repeat, n, refineRepeatRedo)
		}
		if !repeat && slices.Max(qps) != refineQPs[len(refineQPs)-1] {
			t.Fatalf("passes after a caret blink %v, want only the finest", qps)
		}
	}
}

// losslessKbps is the least bandwidth over which a lossless pass predicted
// from a first finest pass of finest bytes goes out within refineLosslessSend.
func losslessKbps(finest int) int {
	bits := refineLosslessRatio * finest * 8
	return int(math.Ceil(float64(bits) / refineLosslessSend.Seconds() / 1000))
}

// A converged refinement ends with one lossless pass when its predicted size
// fits the bandwidth; the prediction is the first finest pass of the current
// content, measured again after new content.
func TestRefinerEndsWithALosslessPassThatFitsTheBandwidth(t *testing.T) {
	finest := refineQPs[len(refineQPs)-1]
	ladder := append(slices.Clone(refineQPs), finest)
	for _, c := range []struct {
		name      string
		firstPass int // size of the first finest pass
		kbps      int
		lossless  bool
	}{
		{"text page at its bandwidth", 5_000, losslessKbps(5_000), true},
		{"just over the bandwidth", 5_000, losslessKbps(5_000) - 1, false},
		{"photo on a fast link", 17_000, losslessKbps(17_000), true},
		{"no lossless encoder", 5_000, 0, false},
	} {
		t0 := time.Unix(1000, 0)
		r := refiner{losslessKbps: c.kbps}
		r.onFrame(t0, 50_000, 0, 4000)
		var qps []int
		out := func(i int) (int, platform.FrameFlags) {
			switch {
			case i < len(refineQPs)-1:
				return 15_000, platform.FrameStatic
			case i == len(refineQPs)-1:
				return c.firstPass, platform.FrameStatic
			case i == len(refineQPs):
				return refineSettled - 1, platform.FrameStatic
			}
			return 400_000, platform.FrameStatic // the lossless pass
		}
		refineRun(&r, t0, time.Minute, out, nil, &qps)
		want := ladder
		if c.lossless {
			want = append(slices.Clone(ladder), platform.RefineLossless)
		}
		if !slices.Equal(qps, want) {
			t.Fatalf("%s: passes %v, want %v", c.name, qps, want)
		}
	}

	// New content whose first finest pass is twice as large no longer fits.
	t0 := time.Unix(1000, 0)
	r := refiner{losslessKbps: losslessKbps(5_000)}
	r.onFrame(t0, 50_000, 0, 4000)
	pass := func(size int) func(int) (int, platform.FrameFlags) {
		return func(i int) (int, platform.FrameFlags) {
			if i == len(refineQPs)-1 {
				return size, platform.FrameStatic
			}
			return refineSettled - 1, platform.FrameStatic
		}
	}
	var qps []int
	refineRun(&r, t0, time.Minute, pass(5_000), nil, &qps)
	now := t0.Add(time.Minute)
	r.onFrame(now, 50_000, 0, 4000)
	qps = nil
	refineRun(&r, now, time.Minute, pass(10_000), nil, &qps)
	if slices.Contains(qps, platform.RefineLossless) {
		t.Fatalf("passes %v after new content with a larger finest pass, want no lossless one", qps)
	}
}

// A caret blink after the lossless pass makes the picture lossy again where
// it changed: the finest pass converges on it and the lossless pass follows.
func TestRefinerRedoesTheLosslessPassAfterASmallChange(t *testing.T) {
	t0 := time.Unix(1000, 0)
	r := refiner{losslessKbps: losslessKbps(5_000)}
	r.onFrame(t0, 50_000, 0, 4000)
	pass := func(i int) (int, platform.FrameFlags) { return refineSettled - 1, platform.FrameStatic }
	var qps []int
	refineRun(&r, t0, time.Minute, pass, nil, &qps)
	if qps[len(qps)-1] != platform.RefineLossless {
		t.Fatalf("passes %v, want a lossless one last", qps)
	}
	now := t0.Add(time.Minute)
	r.onFrame(now, 900, platform.FrameMinor, 4000)
	qps = nil
	refineRun(&r, now, time.Minute, pass, nil, &qps)
	if want := []int{refineQPs[len(refineQPs)-1], platform.RefineLossless}; !slices.Equal(qps, want) {
		t.Fatalf("passes after a caret blink %v, want %v", qps, want)
	}
}
