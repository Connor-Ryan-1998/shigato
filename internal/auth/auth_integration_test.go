//go:build integration

package auth_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/testkit"
)

func TestLoginWrongPasswordGeneric(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	e.User("known@example.com", testkit.WithPassword("correct-password"))

	_, err1 := auth.Login(ctx, pool, "known@example.com", "wrong-password")
	_, err2 := auth.Login(ctx, pool, "unknown@example.com", "whatever-password")

	if err1 != auth.ErrInvalidCredentials {
		t.Errorf("wrong password: err = %v, want ErrInvalidCredentials", err1)
	}
	if err2 != auth.ErrInvalidCredentials {
		t.Errorf("unknown email: err = %v, want ErrInvalidCredentials", err2)
	}
}

func TestLoginSucceedsAndLogoutRevokes(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	e.User("staffer@example.com", testkit.WithPassword("correct-password"))

	token, err := auth.Login(ctx, pool, "staffer@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("Login returned empty token")
	}

	if _, err := auth.UserIDForToken(ctx, pool, token); err != nil {
		t.Fatalf("session should be valid right after login: %v", err)
	}

	if err := auth.Logout(ctx, pool, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if _, err := auth.UserIDForToken(ctx, pool, token); err == nil {
		t.Fatal("session should be gone after logout")
	}
}

// probeMux mounts a RequireOrg(PermReportsRead) probe route on a, so a test
// can check whether a session token still authenticates.
func probeMux(a *auth.Auth) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	return mux
}

func TestExpiredSession401(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	org := e.Org("acme")
	owner := e.User("owner@example.com", testkit.WithPassword("correct-password"))
	e.Member(owner, org, auth.Owner)

	token := "expiredtoken0000000000000000000000000000000000000000000000000"
	_, err := pool.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now() - interval '1 hour')",
		auth.HashToken(token), owner.ID,
	)
	if err != nil {
		t.Fatalf("inserting expired session: %v", err)
	}

	mux := probeMux(auth.New(pool, false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/probe", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestLoginHandlerSetsSecureCookieOnlyWhenConfigured(t *testing.T) {
	e := testkit.New(t)
	e.User("staffer@example.com", testkit.WithPassword("correct-password"))

	a := auth.New(e.Pool, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"email":"staffer@example.com","password":"correct-password"}`))
	rr := httptest.NewRecorder()
	a.LoginHandler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
		t.Fatalf("cookies = %+v, want one %s cookie", cookies, auth.SessionCookieName)
	}
	if !cookies[0].Secure {
		t.Error("cookie should be Secure when secureCookies is true")
	}
	if !cookies[0].HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if cookies[0].SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookies[0].SameSite)
	}
}

func loginRequest(email, password string) *http.Request {
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	return httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
}

// TestLoginRateLimitedByEmail checks global-constraints.md's "at most 5
// failed logins per email per 15 minutes": a 6th attempt (even with the
// correct password) is 429'd, and the limit lifts once the window has
// passed. It is the test that the login endpoint is wired to the limiter.
func TestLoginRateLimitedByEmail(t *testing.T) {
	e := testkit.New(t)
	e.User("ratelimited@example.com", testkit.WithPassword("correct-password"))

	now := time.Now()
	a := auth.New(e.Pool, false, auth.WithClock(func() time.Time { return now }))

	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, loginRequest("ratelimited@example.com", "wrong-password"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want %d", i, rr.Code, http.StatusUnauthorized)
		}
	}

	rr := httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("ratelimited@example.com", "correct-password"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error != "too many attempts, try again later" {
		t.Errorf("error = %q, want %q", body.Error, "too many attempts, try again later")
	}

	// Past the 15 minute window, the correct password succeeds again.
	now = now.Add(15*time.Minute + time.Second)
	rr = httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("ratelimited@example.com", "correct-password"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status after window elapses = %d, want %d", rr.Code, http.StatusNoContent)
	}
}

// TestLoginRateLimitedByIP checks the 20-per-IP-per-15-minutes limit: 20
// failed attempts against distinct (unknown) emails, all from the same
// RemoteAddr (httptest.NewRequest's default), get a 21st attempt 429'd
// regardless of which email it names.
func TestLoginRateLimitedByIP(t *testing.T) {
	e := testkit.New(t)

	now := time.Now()
	a := auth.New(e.Pool, false, auth.WithClock(func() time.Time { return now }))

	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, loginRequest(fmt.Sprintf("unknown-%d@example.com", i), "whatever"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want %d", i, rr.Code, http.StatusUnauthorized)
		}
	}

	rr := httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("yet-another@example.com", "whatever"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}
}

// TestLoginSuccessDoesNotResetRateLimit checks global-constraints.md /
// Ruling: "Success does not reset either count." A successful login in the
// middle of a run of failures only releases its own reservation - it
// doesn't zero out the counter - so the failures on both sides of it still
// add up to the limit.
func TestLoginSuccessDoesNotResetRateLimit(t *testing.T) {
	e := testkit.New(t)
	e.User("resets-nothing@example.com", testkit.WithPassword("correct-password"))

	now := time.Now()
	a := auth.New(e.Pool, false, auth.WithClock(func() time.Time { return now }))

	for i := 0; i < 4; i++ {
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, loginRequest("resets-nothing@example.com", "wrong-password"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d, want %d", i, rr.Code, http.StatusUnauthorized)
		}
	}

	rr := httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("resets-nothing@example.com", "correct-password"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("successful login: status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	// A 5th failure, right after the success: 4 + 1 = 5, at the limit.
	rr = httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("resets-nothing@example.com", "wrong-password"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("5th failure: status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	// A 6th attempt - even the correct password - is blocked: the earlier
	// success never reset the count back down.
	rr = httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("resets-nothing@example.com", "correct-password"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status after success mid-run = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
}

// TestLoginRateLimitEmailIsCaseInsensitive checks that the email rate-limit
// key is built from the lowercased address, so varying the case of repeated
// failed attempts can't be used to dodge the limit.
func TestLoginRateLimitEmailIsCaseInsensitive(t *testing.T) {
	e := testkit.New(t)

	now := time.Now()
	a := auth.New(e.Pool, false, auth.WithClock(func() time.Time { return now }))

	emails := []string{
		"Mixed-Case@Example.com",
		"mixed-case@example.com",
		"MIXED-CASE@EXAMPLE.COM",
		"mixed-Case@example.Com",
		"MiXeD-cAsE@eXaMpLe.CoM",
	}
	for i, email := range emails {
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, loginRequest(email, "wrong-password"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d (%s): status = %d, want %d", i, email, rr.Code, http.StatusUnauthorized)
		}
	}

	rr := httptest.NewRecorder()
	a.LoginHandler(rr, loginRequest("mixed-case@example.com", "whatever"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d (case variants should share one limit)", rr.Code, http.StatusTooManyRequests)
	}
}

func TestLoginEmailBlockedAttemptsCountAgainstIP(t *testing.T) {
	e := testkit.New(t)
	now := time.Now()
	a := auth.New(e.Pool, false, auth.WithClock(func() time.Time { return now }))
	for i := 0; i < auth.LoginEmailLimit; i++ {
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, loginRequest("blocked@example.com", "wrong-password"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("prime email attempt %d: %d", i, rr.Code)
		}
	}
	for i := 0; i < auth.LoginIPLimit; i++ {
		req := loginRequest("blocked@example.com", "wrong-password")
		req.RemoteAddr = "192.0.2.5:12345"
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("blocked attempt %d: %d", i, rr.Code)
		}
	}
	req := loginRequest("fresh@example.com", "wrong-password")
	req.RemoteAddr = "192.0.2.5:12345"
	rr := httptest.NewRecorder()
	a.LoginHandler(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("fresh email from blocked IP: %d, want 429", rr.Code)
	}
}

func TestLogoutHandlerRevokesSession(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	org := e.Org("acme")
	staffer := e.User("staffer@example.com", testkit.WithPassword("correct-password"))
	e.Member(staffer, org, auth.Owner)

	token, err := auth.Login(ctx, pool, "staffer@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	a := auth.New(pool, false)

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	logoutRR := httptest.NewRecorder()
	a.LogoutHandler(logoutRR, logoutReq)

	if logoutRR.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d, body=%s", logoutRR.Code, http.StatusNoContent, logoutRR.Body.String())
	}

	probeReq := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/probe", nil)
	probeReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	probeRR := httptest.NewRecorder()
	probeMux(a).ServeHTTP(probeRR, probeReq)

	if probeRR.Code != http.StatusUnauthorized {
		t.Errorf("status after logout = %d, want %d", probeRR.Code, http.StatusUnauthorized)
	}
}
