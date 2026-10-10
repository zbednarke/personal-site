package main

import (
	"sync"
	"time"
)

// limiter counts failed password attempts in memory, per client and overall.
// It resets when the service restarts, which is acceptable for a fallback
// that is off by default.
type limiter struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	perIP    int
	global   int
	failures map[string][]time.Time
	all      []time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, window: 15 * time.Minute, perIP: 5, global: 30, failures: map[string][]time.Time{}}
}

func recent(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-l.window)
	l.all = recent(l.all, cutoff)
	if list := recent(l.failures[key], cutoff); len(list) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = list
	}
	return len(l.failures[key]) < l.perIP && len(l.all) < l.global
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) > 10000 {
		l.failures = map[string][]time.Time{}
	}
	now := l.now()
	l.failures[key] = append(l.failures[key], now)
	l.all = append(l.all, now)
}
