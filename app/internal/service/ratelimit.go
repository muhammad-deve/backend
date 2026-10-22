package service

import (
	"sync"
	"time"
)

// rateLimiter is an in-memory sliding-window limiter keyed by a string such as
// an email, an IP or a user id. It allows at most max hits per window and can
// also require a cooldown between hits. State resets on restart.
type rateLimiter struct {
	mu       sync.Mutex
	hits     map[string][]time.Time
	window   time.Duration
	max      int
	cooldown time.Duration
	lastGC   time.Time
	now      func() time.Time
}

func newRateLimiter(window time.Duration, max int, cooldown time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:     make(map[string][]time.Time),
		window:   window,
		max:      max,
		cooldown: cooldown,
		now:      time.Now,
	}
}

// Allow records a hit for key when it is within the limits. When it returns
// false, retryAfter says how long to wait.
func (l *rateLimiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	times := l.current(key, now)
	if wait := l.wait(times, now); wait > 0 {
		return false, wait
	}
	l.hits[key] = append(times, now)
	return true, 0
}

// Blocked reports whether key is at its limit, without recording a hit.
func (l *rateLimiter) Blocked(key string) (blocked bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	wait := l.wait(l.current(key, now), now)
	return wait > 0, wait
}

// Add records a hit for key without checking the limits.
func (l *rateLimiter) Add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.hits[key] = append(l.current(key, now), now)
}

// Reset forgets every hit for key.
func (l *rateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// current returns the hits for key that are still inside the window. It also
// drops stale keys now and then so the map can't grow forever.
func (l *rateLimiter) current(key string, now time.Time) []time.Time {
	if now.Sub(l.lastGC) > l.window {
		for k, times := range l.hits {
			if pruned := l.prune(times, now); len(pruned) == 0 {
				delete(l.hits, k)
			} else {
				l.hits[k] = pruned
			}
		}
		l.lastGC = now
	}
	times := l.prune(l.hits[key], now)
	if len(times) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = times
	}
	return times
}

// wait returns how long key must wait before its next hit, or 0.
func (l *rateLimiter) wait(times []time.Time, now time.Time) time.Duration {
	if len(times) == 0 {
		return 0
	}
	if l.cooldown > 0 {
		if since := now.Sub(times[len(times)-1]); since < l.cooldown {
			return l.cooldown - since
		}
	}
	if len(times) >= l.max {
		if retry := l.window - now.Sub(times[len(times)-l.max]); retry > 0 {
			return retry
		}
		return time.Second
	}
	return 0
}

// prune drops hits that have fallen outside the window.
func (l *rateLimiter) prune(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return times
	}
	return append([]time.Time(nil), times[i:]...)
}
