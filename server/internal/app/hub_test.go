package app

import (
	"testing"
	"time"
)

// A device flapping for days with nobody reading the metric must not grow
// the reconnect log without bound: trimming keeps only the last minute.
func TestReconnectLogKeepsTheLastMinute(t *testing.T) {
	h := &Hub{}
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for i := range 3600 { // one reconnect a second for an hour
		now := t0.Add(time.Duration(i) * time.Second)
		h.trimReconnectsLocked(now)
		h.reconnects = append(h.reconnects, now)
	}
	if n := len(h.reconnects); n != 61 { // [now-60 s, now]
		t.Fatalf("%d reconnects kept, want 61", n)
	}
}
