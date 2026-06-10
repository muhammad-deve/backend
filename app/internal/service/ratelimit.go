package service

import (
	"sync"
	"time"
)

// otpRateLimiter is an in-memory sliding-window limiter for OTP sends, keyed by
// a normalized identifier (email). It enforces two rules:
//   - a minimum cooldown between consecutive sends, and
//   - a maximum number of sends within a rolling window.
//
// State is kept in memory only, so it resets on restart. That is acceptable for
// OTP abuse protection and avoids any external dependency.
type otpRateLimiter struct {
	mu       sync.Mutex
	hits     map[string][]time.Time
	window   time.Duration
	maxInWin int
	cooldown time.Duration
	lastSeen time.Time
}

func newOTPRateLimiter(window time.Duration, maxInWindow int, cooldown time.Duration) *otpRateLimiter {
	return &otpRateLimiter{
		hits:     make(map[string][]time.Time),
		window:   window,
		maxInWin: maxInWindow,
		cooldown: cooldown,
	}
}

// Allow reports whether a send is permitted for the given key right now. When it
// returns false, retryAfter indicates how long the caller should wait before the
// next attempt is likely to succeed.
func (l *otpRateLimiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Opportunistic cleanup of stale keys to keep the map from growing forever.
	if now.Sub(l.lastSeen) > l.window {
		l.gc(now)
	}
	l.lastSeen = now

	times := l.prune(l.hits[key], now)

	// Cooldown: enforce a minimum gap since the most recent send.
	if len(times) > 0 {
		if since := now.Sub(times[len(times)-1]); since < l.cooldown {
			l.hits[key] = times
			return false, l.cooldown - since
		}
	}

	// Window cap: limit total sends within the rolling window.
	if len(times) >= l.maxInWin {
		retry := l.window - now.Sub(times[0])
		if retry < 0 {
			retry = 0
		}
		l.hits[key] = times
		return false, retry
	}

	l.hits[key] = append(times, now)
	return true, 0
}

// prune drops timestamps that have fallen outside the rolling window.
func (l *otpRateLimiter) prune(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	if i == 0 {
		return times
	}
	return append([]time.Time(nil), times[i:]...)
}

// gc removes keys whose timestamps have all expired.
func (l *otpRateLimiter) gc(now time.Time) {
	for key, times := range l.hits {
		if pruned := l.prune(times, now); len(pruned) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = pruned
		}
	}
}
