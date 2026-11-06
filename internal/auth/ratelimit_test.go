package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
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
		l.Fail("k", time.Minute)
	}

	ok, retryAfter := l.Allow("k", 5, time.Minute)
	if ok {
		t.Fatal("6th attempt within window: want blocked")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Errorf("retryAfter = %v, want in (0, 1m]", retryAfter)
	}

	// A different key is unaffected by k's failures.
	if ok, _ := l.Allow("other", 5, time.Minute); !ok {
		t.Error("unrelated key: want allowed")
	}

	// Advancing the clock past the window lets k's failures age out.
	now = now.Add(time.Minute + time.Second)
	if ok, _ := l.Allow("k", 5, time.Minute); !ok {
		t.Error("after window elapses: want allowed again")
	}
}

func TestClientIPTrustedProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		trusted    *net.IPNet
		want       string
	}{
		{
			name:       "no trusted proxy configured: XFF ignored",
			remoteAddr: "203.0.113.5:1234",
			xff:        "198.51.100.9",
			trusted:    nil,
			want:       "203.0.113.5",
		},
		{
			name:       "remote outside trusted CIDR: XFF ignored",
			remoteAddr: "203.0.113.5:1234",
			xff:        "198.51.100.9",
			trusted:    trusted,
			want:       "203.0.113.5",
		},
		{
			name:       "remote inside trusted CIDR: right-most untrusted XFF hop used",
			remoteAddr: "10.0.0.1:1234",
			xff:        "198.51.100.9, 10.0.0.2",
			trusted:    trusted,
			want:       "198.51.100.9",
		},
		{
			name:       "remote inside trusted CIDR, no untrusted hop: falls back to RemoteAddr",
			remoteAddr: "10.0.0.1:1234",
			xff:        "10.0.0.2, 10.0.0.3",
			trusted:    trusted,
			want:       "10.0.0.1",
		},
		{
			name:       "remote inside trusted CIDR, no XFF header: falls back to RemoteAddr",
			remoteAddr: "10.0.0.1:1234",
			xff:        "",
			trusted:    trusted,
			want:       "10.0.0.1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := ClientIP(r, c.trusted); got != c.want {
				t.Errorf("ClientIP() = %q, want %q", got, c.want)
			}
		})
	}
}
