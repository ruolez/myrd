package session

import (
	"slices"
	"testing"
	"time"
)

func TestRefinerWaitsForAQuietScreen(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	if _, ok := r.due(t0); ok {
		t.Fatal("refined before any frame was encoded")
	}
	r.onFrame(t0, 50_000, false, false, 4000)
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
	r.onFrame(t0, 50_000, true, false, kbps)
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
		r.onFrame(now, size, false, true, kbps)
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
		name             string
		keyframe, static bool
	}{
		{"new content", false, false},
		{"keyframe (PLI) while static", true, true},
	} {
		t0 := time.Unix(1000, 0)
		var r refiner
		r.onFrame(t0, 50_000, false, false, 4000)
		now := t0.Add(refineIdle)
		r.due(now)
		r.onFrame(now, 1000, false, true, 4000)
		now = now.Add(refineMinGap)
		r.due(now)
		r.onFrame(now, 50_000, c.keyframe, c.static, 4000)
		if _, ok := r.due(now.Add(refineIdle - time.Millisecond)); ok {
			t.Fatalf("%s: refined before the screen was quiet again", c.name)
		}
		if qp, _ := r.due(now.Add(refineIdle)); qp != refineQPs[0] {
			t.Fatalf("%s: next pass qp %d, want the coarsest (%d)", c.name, qp, refineQPs[0])
		}
	}
}

// A caret blink or a clock tick touches a few macroblocks: the rest of the
// screen keeps its refined quality, so only the finest passes are redone.
func TestRefinerBacksUpAfterASmallChange(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, false, false, 4000)
	now := t0.Add(refineIdle)
	for range 4 { // passes 36, 33, 30, 27
		r.due(now)
		r.onFrame(now, 500, false, true, 4000)
		now = now.Add(refineMinGap)
	}
	r.onFrame(now, refineSmall-1, false, false, 4000)
	if _, ok := r.due(now.Add(refineIdle - time.Millisecond)); ok {
		t.Fatal("refined before the screen was quiet again")
	}
	if qp, _ := r.due(now.Add(refineIdle)); qp != refineQPs[2] {
		t.Fatalf("after a small change: qp %d, want %d (two passes back)", qp, refineQPs[2])
	}
}

func TestRefinerRetriesAPassThatNeverCameBack(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var r refiner
	r.onFrame(t0, 50_000, false, false, 4000)
	now := t0.Add(refineIdle)
	first, _ := r.due(now)
	if _, ok := r.due(now.Add(refineLost - time.Millisecond)); ok {
		t.Fatal("next pass while the previous one may still arrive")
	}
	if qp, ok := r.due(now.Add(refineLost)); !ok || qp != refineQPs[1] {
		t.Fatalf("after a lost pass (%d): due = %d, %v; want %d", first, qp, ok, refineQPs[1])
	}
}
