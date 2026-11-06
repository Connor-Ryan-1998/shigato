package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter tracks failed attempts per key in a sliding time window. It's used
// to rate-limit login attempts by email and by client IP.
//
// ponytail: in-memory limiter, per instance; move to Postgres if shigoto
// ever runs >1 replica.
type Limiter struct {
	now func() time.Time

	mu    sync.Mutex
	fails map[string][]time.Time
}

// NewLimiter creates a Limiter. now is normally time.Now; tests inject a
// fake clock so a sliding window can be exercised without a real sleep.
func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{now: now, fails: make(map[string][]time.Time)}
}

// Allow reports whether key has fewer than max recorded failures within the
// trailing window. When it doesn't, ok is false and retryAfter is how long
// until the oldest failure in the window ages out. Expired failures for key
// are pruned as a side effect.
func (l *Limiter) Allow(key string, max int, window time.Duration) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	times := l.prune(key, window)
	if len(times) < max {
		return true, 0
	}
	retryAfter = times[0].Add(window).Sub(l.now())
	if retryAfter < 0 {
		retryAfter = 0
	}
	return false, retryAfter
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string, window time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	times := l.prune(key, window)
	l.fails[key] = append(times, l.now())
}

// prune drops entries older than window from l.fails[key], stores and
// returns the remainder. Caller must hold l.mu.
func (l *Limiter) prune(key string, window time.Duration) []time.Time {
	cutoff := l.now().Add(-window)
	times := l.fails[key]

	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	times = times[i:]

	if len(times) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = times
	return times
}

// ClientIP returns the request's client IP. If trusted is nil, or
// r.RemoteAddr isn't inside it, the result is always r.RemoteAddr: a
// client-supplied X-Forwarded-For is never trusted from an untrusted
// source. Otherwise it walks X-Forwarded-For from the right and returns the
// right-most entry that isn't itself inside trusted (the nearest hop the
// trusted proxy chain didn't add); if every entry is inside trusted, it
// falls back to RemoteAddr.
func ClientIP(r *http.Request, trusted *net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trusted == nil {
		return host
	}

	remoteIP := net.ParseIP(host)
	if remoteIP == nil || !trusted.Contains(remoteIP) {
		return host
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	hops := strings.Split(xff, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(hops[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			continue
		}
		if !trusted.Contains(ip) {
			return candidate
		}
	}
	return host
}
