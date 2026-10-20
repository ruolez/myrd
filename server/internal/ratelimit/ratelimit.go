// Package ratelimit implements the in-memory limiters: a sliding-window
// failure counter (login attempts) and keyed token buckets (per-IP API calls).
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Failures counts failures per key in a sliding window.
type Failures struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
	now    func() time.Time
}

func NewFailures(max int, window time.Duration) *Failures {
	return &Failures{max: max, window: window, hits: map[string][]time.Time{}, now: time.Now}
}

func (f *Failures) prune(key string, now time.Time) []time.Time {
	h := f.hits[key]
	i := 0
	for i < len(h) && now.Sub(h[i]) >= f.window {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(f.hits, key)
	} else {
		f.hits[key] = h
	}
	return h
}

// Blocked reports whether any key has reached the failure limit.
func (f *Failures) Blocked(keys ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for _, k := range keys {
		if len(f.prune(k, now)) >= f.max {
			return true
		}
	}
	return false
}

func (f *Failures) Fail(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for _, k := range keys {
		f.hits[k] = append(f.prune(k, now), now)
	}
}

func (f *Failures) Reset(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.hits, k)
	}
}

// Buckets is a set of token buckets keyed by string (e.g. client IP).
type Buckets struct {
	mu    sync.Mutex
	r     rate.Limit
	burst int
	m     map[string]*entry
}

type entry struct {
	l    *rate.Limiter
	seen time.Time
}

func NewBuckets(perSecond float64, burst int) *Buckets {
	return &Buckets{r: rate.Limit(perSecond), burst: burst, m: map[string]*entry{}}
}

func (b *Buckets) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	e, ok := b.m[key]
	if !ok {
		if len(b.m) > 10000 {
			for k, v := range b.m {
				if now.Sub(v.seen) > 10*time.Minute {
					delete(b.m, k)
				}
			}
		}
		e = &entry{l: rate.NewLimiter(b.r, b.burst)}
		b.m[key] = e
	}
	e.seen = now
	return e.l.Allow()
}
