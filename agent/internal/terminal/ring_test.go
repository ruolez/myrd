package terminal

import (
	"bytes"
	"math/rand"
	"testing"
)

// The ring always holds exactly the last min(size, total) bytes written,
// whatever the write sizes.
func TestRingHoldsTail(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		size := 1 + rng.Intn(64)
		r := newRing(size)
		var all []byte
		for w := 0; w < 1+rng.Intn(20); w++ {
			p := make([]byte, rng.Intn(3*size))
			rng.Read(p)
			r.write(p)
			all = append(all, p...)
		}
		want := all
		if len(want) > size {
			want = want[len(want)-size:]
		}
		if got := r.since(0); !bytes.Equal(got, want) {
			t.Fatalf("trial %d (size %d, total %d): since(0) = %x, want %x", trial, size, len(all), got, want)
		}
		if r.total != int64(len(all)) || r.start() != int64(len(all)-len(want)) {
			t.Fatalf("trial %d: total %d start %d", trial, r.total, r.start())
		}
		if mid := r.start() + int64(len(want)/2); !bytes.Equal(r.since(mid), all[mid:]) {
			t.Fatalf("trial %d: since(%d) wrong", trial, mid)
		}
	}
}

func TestRingSinceEnd(t *testing.T) {
	r := newRing(8)
	r.write([]byte("abc"))
	if got := r.since(3); got != nil {
		t.Fatalf("since(end) = %q, want nil", got)
	}
}
