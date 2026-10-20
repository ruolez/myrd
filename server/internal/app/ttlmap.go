package app

import (
	"sync"
	"time"
)

// ttlMap holds short-lived single-use values (login tickets, WebAuthn
// ceremonies, agent nonces). Take removes the value: everything is single use.
type ttlMap[V any] struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	v   V
	exp time.Time
}

func newTTLMap[V any](ttl time.Duration) *ttlMap[V] {
	return &ttlMap[V]{ttl: ttl, m: map[string]ttlEntry[V]{}}
}

func (t *ttlMap[V]) Put(key string, v V) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if len(t.m) > 5000 {
		for k, e := range t.m {
			if now.After(e.exp) {
				delete(t.m, k)
			}
		}
	}
	t.m[key] = ttlEntry[V]{v: v, exp: now.Add(t.ttl)}
}

func (t *ttlMap[V]) Take(key string) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[key]
	delete(t.m, key)
	var zero V
	if !ok || time.Now().After(e.exp) {
		return zero, false
	}
	return e.v, true
}
