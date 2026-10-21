package session

import (
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

// A static screen walks every pass once, each sent after the previous one
// came back and its bits had time to drain at refineShare of the target.
func TestRefinerPacesPassesToTheBitrate(t *testing.T) {
	const kbps = 4000 // 2 Mbps for refinement: 25 KB takes 100 ms, 100 KB takes 400 ms
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, platform.FrameKey, kbps)
	sizes := []int{1_000, 25_000, 100_000, 50_000, 10_000, 5_000, 2_000}
	wantGap := []time.Duration{refineMinGap, refineMinGap, 400 * time.Millisecond, 200 * time.Millisecond,
		refineMinGap, refineMinGap}
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
	if !slices.Equal(qps, refineQPs) {
		t.Fatalf("passes = %v, want %v", qps, refineQPs)
	}
	if _, ok := r.due(now.Add(time.Hour)); ok {
		t.Fatal("refined past the finest pass")
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
		for range 4 { // passes 36, 33, 30, 27
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
		if qp, _ := r.due(now.Add(refineIdle)); qp != refineQPs[4] {
			t.Fatalf("%s: qp %d, want %d (the next pass)", c.name, qp, refineQPs[4])
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
