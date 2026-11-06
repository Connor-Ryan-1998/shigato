package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sweepInterval bounds how often an access to any key also sweeps the whole
// map for keys that have gone stale (their newest entry is older than the
// longest window ever checked) - otherwise a key that's hit once and never
// checked again (e.g. a one-off IP) would never be pruned by Allow/Succeed,
// which only prune the one key they're called with.
const sweepInterval = time.Minute

// Limiter tracks attempts per key in a sliding time window. It's used to
// rate-limit login attempts by email and by client IP.
//
// Allow both checks and reserves a slot atomically (under one lock), so
// concurrent callers can't all observe "under the limit" before any of them
// records its attempt - a plain check-then-record pattern across two calls
// would let N concurrent requests all pass. A reservation that turns out to
// be a success is released with Succeed; one that's a failure is simply
// left in place, since Allow already recorded it.
//
// ponytail: in-memory limiter, per instance; move to Postgres if shigoto
// ever runs >1 replica.
type Limiter struct {
	now func() time.Time

	mu        sync.Mutex
	fails     map[string][]time.Time
	maxWindow time.Duration
	lastSweep time.Time
}

// NewLimiter creates a Limiter. now is normally time.Now; tests inject a
// fake clock so a sliding window can be exercised without a real sleep.
func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{now: now, fails: make(map[string][]time.Time), lastSweep: now()}
}

// Allow reports whether key has fewer than max recorded attempts within the
// trailing window, and - if so - immediately records this one (reserving a
// slot), so a concurrent caller checking the same key right after sees the
// updated count. When it doesn't, ok is false and retryAfter is how long
// until the oldest attempt in the window ages out; no reservation is made
// in that case.
//
// A call that turns out to be a success should be matched with Succeed, so
// it doesn't count towards the limit; a failure needs no further call -
// the reservation Allow already made is the record of it.
func (l *Limiter) Allow(key string, max int, window time.Duration) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(window)

	times := l.prune(key, window)
	if len(times) >= max {
		retryAfter = times[0].Add(window).Sub(l.now())
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter
	}

	l.fails[key] = append(times, l.now())
	return true, 0
}

// Succeed releases one reservation Allow made for key (the most recent
// one), so a successful attempt doesn't count towards the limit. Calling it
// without a matching outstanding Allow reservation is a harmless no-op.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	times := l.fails[key]
	if len(times) == 0 {
		return
	}
	times = times[:len(times)-1]
	if len(times) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = times
	}
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

// sweep drops every key whose newest entry is older than the longest window
// ever passed to Allow, at most once per sweepInterval. Caller must hold
// l.mu. Unlike prune, which only ever looks at the one key it's called
// with, this catches keys that were hit once and never checked again.
func (l *Limiter) sweep(window time.Duration) {
	if window > l.maxWindow {
		l.maxWindow = window
	}

	now := l.now()
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now

	cutoff := now.Add(-l.maxWindow)
	for key, times := range l.fails {
		if len(times) == 0 || times[len(times)-1].Before(cutoff) {
			delete(l.fails, key)
		}
	}
}

// ipRateLimitKey builds the limiter key for a client IP (config.go's
// SHIGOTO_TRUSTED_PROXY / ClientIP). IPv6 addresses are keyed by their /64
// - the block a residential or mobile ISP typically hands one customer -
// rather than the full address, which a client can often change at will
// within their own /64.
func ipRateLimitKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "ip:" + ip
	}
	if parsed.To4() != nil {
		return "ip:" + parsed.String()
	}
	network := parsed.Mask(net.CIDRMask(64, 128))
	return "ip:" + network.String() + "/64"
}

// ClientIP returns the request's client IP, normalised (e.g.
// ::ffff:1.2.3.4 as it reads from Header values). If trusted is nil, or
// r.RemoteAddr isn't inside it, the result is always r.RemoteAddr: a
// client-supplied X-Forwarded-For is never trusted from an untrusted
// source. Otherwise it walks X-Forwarded-For (every occurrence of the
// header, joined in order) from the right and returns the right-most entry
// that isn't itself inside trusted (the nearest hop the trusted proxy chain
// didn't add). It stops at - and ignores - the first hop it can't parse as
// an IP, falling back to RemoteAddr rather than risk skipping over a
// malformed entry to a client-controlled one further left; the same
// fallback applies if every entry is inside trusted.
func ClientIP(r *http.Request, trusted *net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(host)

	fallback := host
	if remoteIP != nil {
		fallback = remoteIP.String()
	}

	if trusted == nil || remoteIP == nil || !trusted.Contains(remoteIP) {
		return fallback
	}

	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if xff == "" {
		return fallback
	}
	hops := strings.Split(xff, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(hops[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			return fallback
		}
		if !trusted.Contains(ip) {
			return ip.String()
		}
	}
	return fallback
}
