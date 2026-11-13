package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLimiterSlidingWindow(t *testing.T) {
	now := time.Now()
	l := NewLimiter(func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("k", 5, time.Minute); !ok {
			t.Fatalf("attempt %d: want allowed", i)
		}
	}

	ok, retryAfter := l.Allow("k", 5, time.Minute)
	if ok {
		t.Fatal("6th attempt within window: want blocked")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Errorf("retryAfter = %v, want in (0, 1m]", retryAfter)
	}

	// A different key is unaffected by k's attempts.
	if ok, _ := l.Allow("other", 5, time.Minute); !ok {
		t.Error("unrelated key: want allowed")
	}

	// Advancing the clock past the window lets k's attempts age out.
	now = now.Add(time.Minute + time.Second)
	if ok, _ := l.Allow("k", 5, time.Minute); !ok {
		t.Error("after window elapses: want allowed again")
	}
}

// TestLimiterSucceedDoesNotCount checks that a released (successful)
// reservation doesn't count towards the limit: an unbounded number of
// Allow+Succeed pairs never blocks a key, only unreleased (failed) ones do.
func TestLimiterSucceedDoesNotCount(t *testing.T) {
	now := time.Now()
	l := NewLimiter(func() time.Time { return now })

	for i := 0; i < 10; i++ {
		ok, _ := l.Allow("k", 5, time.Minute)
		if !ok {
			t.Fatalf("succeeding attempt %d: want allowed", i)
		}
		l.Succeed("k")
	}

	// 5 real (unreleased) failures still fit within the limit...
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("k", 5, time.Minute); !ok {
			t.Fatalf("failing attempt %d: want allowed", i)
		}
	}
	// ...and a 6th is blocked, same as if the 10 successes never happened.
	if ok, _ := l.Allow("k", 5, time.Minute); ok {
		t.Error("6th unreleased attempt: want blocked")
	}
}

// TestLimiterConcurrentAllowCapsAtMax is the race regression test: Allow
// must atomically check-and-reserve under its own lock, so N concurrent
// callers against one key can never all observe "under the limit" before
// any of them is recorded. Run with -race.
func TestLimiterConcurrentAllowCapsAtMax(t *testing.T) {
	l := NewLimiter(time.Now)

	const goroutines = 20
	const max = 5

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ok, _ := l.Allow("race-key", max, time.Minute)
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != max {
		t.Errorf("allowed = %d, want exactly %d", allowed, max)
	}
}

// TestLimiterSweepsStaleKeys checks that a key nobody ever checks again
// still gets dropped eventually (sweep), not just a key that's re-checked
// (prune).
func TestLimiterSweepsStaleKeys(t *testing.T) {
	now := time.Now()
	l := NewLimiter(func() time.Time { return now })

	const staleKeys = 50
	for i := 0; i < staleKeys; i++ {
		l.Allow(fmt.Sprintf("stale-%d", i), 5, time.Minute)
	}
	if got := len(l.fails); got != staleKeys {
		t.Fatalf("fails has %d keys, want %d", got, staleKeys)
	}

	// Past both the window and the sweep interval; a single access to any
	// key (not the stale ones) triggers a full-map sweep.
	now = now.Add(2 * time.Minute)
	l.Allow("trigger-sweep", 5, time.Minute)

	if got := len(l.fails); got > 1 {
		t.Errorf("fails has %d keys after sweep, want only trigger-sweep", got)
	}
}

func TestClientIPTrustedProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	_, trustedV6, err := net.ParseCIDR("2001:db8:ffff::/48")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		remoteAddr string
		xff        []string
		trusted    *net.IPNet
		want       string
	}{
		{
			name:       "no trusted proxy configured: XFF ignored",
			remoteAddr: "203.0.113.5:1234",
			xff:        []string{"198.51.100.9"},
			trusted:    nil,
			want:       "203.0.113.5",
		},
		{
			name:       "remote outside trusted CIDR: XFF ignored",
			remoteAddr: "203.0.113.5:1234",
			xff:        []string{"198.51.100.9"},
			trusted:    trusted,
			want:       "203.0.113.5",
		},
		{
			name:       "remote inside trusted CIDR: right-most untrusted XFF hop used",
			remoteAddr: "10.0.0.1:1234",
			xff:        []string{"198.51.100.9, 10.0.0.2"},
			trusted:    trusted,
			want:       "198.51.100.9",
		},
		{
			name:       "remote inside trusted CIDR, no untrusted hop: falls back to RemoteAddr",
			remoteAddr: "10.0.0.1:1234",
			xff:        []string{"10.0.0.2, 10.0.0.3"},
			trusted:    trusted,
			want:       "10.0.0.1",
		},
		{
			name:       "remote inside trusted CIDR, no XFF header: falls back to RemoteAddr",
			remoteAddr: "10.0.0.1:1234",
			xff:        nil,
			trusted:    trusted,
			want:       "10.0.0.1",
		},
		{
			name:       "two X-Forwarded-For header lines are joined before walking",
			remoteAddr: "10.0.0.1:1234",
			xff:        []string{"198.51.100.9", "10.0.0.2"},
			trusted:    trusted,
			want:       "198.51.100.9",
		},
		{
			name:       "unparseable right-most hop stops the walk rather than skipping it",
			remoteAddr: "10.0.0.1:1234",
			xff:        []string{"9.9.9.9, 1.2.3.4:5678"},
			trusted:    trusted,
			want:       "10.0.0.1",
		},
		{
			name:       "bracketed IPv6 RemoteAddr, no trusted proxy",
			remoteAddr: "[2001:db8::1]:1234",
			xff:        nil,
			trusted:    nil,
			want:       "2001:db8::1",
		},
		{
			name:       "bracketed IPv6 RemoteAddr inside a trusted v6 CIDR",
			remoteAddr: "[2001:db8:ffff::1]:1234",
			xff:        []string{"2001:db8::9"},
			trusted:    trustedV6,
			want:       "2001:db8::9",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remoteAddr
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ClientIP(r, c.trusted); got != c.want {
				t.Errorf("ClientIP() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIPRateLimitKey(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want string
	}{
		{"IPv4 keyed by full address", "203.0.113.5", "ip:203.0.113.5"},
		{
			name: "IPv6 keyed by /64",
			ip:   "2001:db8:1234:5678:aaaa:bbbb:cccc:dddd",
			want: "ip:2001:db8:1234:5678::/64",
		},
		{
			name: "two IPv6 addresses in the same /64 share a key",
			ip:   "2001:db8:1234:5678:1111:2222:3333:4444",
			want: "ip:2001:db8:1234:5678::/64",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ipRateLimitKey(c.ip); got != c.want {
				t.Errorf("ipRateLimitKey(%q) = %q, want %q", c.ip, got, c.want)
			}
		})
	}
}

// TestLoginRateLimitRetryAfterCoversBothKeys checks LoginHandler reports the
// later of the email and IP keys' Retry-After when both are blocked. It
// never reaches the database, so it runs as a unit test.
func TestLoginRateLimitRetryAfterCoversBothKeys(t *testing.T) {
	now := time.Now()
	a := New(nil, false, WithClock(func() time.Time { return now }))
	for i := 0; i < loginEmailLimit; i++ {
		a.limiter.Allow("email:blocked@example.com", loginEmailLimit, loginWindow)
	}
	now = now.Add(time.Minute)
	for i := 0; i < loginIPLimit; i++ {
		a.limiter.Allow("ip:192.0.2.5", loginIPLimit, loginWindow)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"email":"blocked@example.com","password":"wrong-password"}`))
	req.RemoteAddr = "192.0.2.5:12345"
	a.LoginHandler(rr, req)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "900" {
		t.Fatalf("status=%d Retry-After=%q, want 429 and 900", rr.Code, rr.Header().Get("Retry-After"))
	}
}
