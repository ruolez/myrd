package ratelimit

import (
	"testing"
	"time"
)

func TestFailuresSlidingWindow(t *testing.T) {
	now := time.Unix(0, 0)
	f := NewFailures(3, 15*time.Minute)
	f.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if f.Blocked("ip:1", "email:a") {
			t.Fatalf("blocked after %d failures", i)
		}
		f.Fail("ip:1", "email:a")
	}
	if !f.Blocked("ip:1") || !f.Blocked("email:a", "ip:other") {
		t.Fatal("expected block once the limit is reached on any key")
	}
	now = now.Add(15 * time.Minute)
	if f.Blocked("ip:1") {
		t.Fatal("window did not slide")
	}
	f.Fail("email:a")
	f.Reset("email:a")
	if f.Blocked("email:a") {
		t.Fatal("reset did not clear")
	}
}

func TestBucketsBurstThenDeny(t *testing.T) {
	b := NewBuckets(0.0001, 2)
	if !b.Allow("k") || !b.Allow("k") || b.Allow("k") {
		t.Fatal("burst of 2 then deny expected")
	}
	if !b.Allow("other") {
		t.Fatal("keys must be independent")
	}
}
