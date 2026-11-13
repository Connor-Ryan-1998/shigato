//go:build integration

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/db"
)

func createTestOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func createTestUser(t *testing.T, pool *pgxpool.Pool, email, password string) int64 {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	var userID int64
	err = pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		email, email, hash,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("inserting user %s: %v", email, err)
	}
	return userID
}

func addMembership(t *testing.T, pool *pgxpool.Pool, userID, orgID int64, role Role) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, orgID, string(role),
	)
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

func TestLoginWrongPasswordGeneric(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	createTestUser(t, pool, "known@example.com", "correct-password")

	_, err1 := Login(ctx, pool, "known@example.com", "wrong-password")
	_, err2 := Login(ctx, pool, "unknown@example.com", "whatever-password")

	if err1 != ErrInvalidCredentials {
		t.Errorf("wrong password: err = %v, want ErrInvalidCredentials", err1)
	}
	if err2 != ErrInvalidCredentials {
		t.Errorf("unknown email: err = %v, want ErrInvalidCredentials", err2)
	}
}

func TestLoginSucceedsAndLogoutRevokes(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	createTestUser(t, pool, "staffer@example.com", "correct-password")

	token, err := Login(ctx, pool, "staffer@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("Login returned empty token")
	}

	if _, err := userIDForToken(ctx, pool, token); err != nil {
		t.Fatalf("session should be valid right after login: %v", err)
	}

	if err := Logout(ctx, pool, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if _, err := userIDForToken(ctx, pool, token); err == nil {
		t.Fatal("session should be gone after logout")
	}
}

func TestExpiredSession401(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "owner@example.com", "correct-password")
	addMembership(t, pool, userID, org.ID, Owner)

	token := "expiredtoken0000000000000000000000000000000000000000000000000"
	_, err := pool.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now() - interval '1 hour')",
		hashToken(token), userID,
	)
	if err != nil {
		t.Fatalf("inserting expired session: %v", err)
	}

	a := New(pool, false)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/probe", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRequireOrgNoSession401(t *testing.T) {
	pool := db.TestPool(t)

	a := New(pool, false)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/probe", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRequireOrgCrossOrg404(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	orgA := createTestOrg(t, pool, "org-a")
	createTestOrg(t, pool, "org-b")
	userID := createTestUser(t, pool, "owner-a@example.com", "correct-password")
	addMembership(t, pool, userID, orgA.ID, Owner)

	token, err := Login(ctx, pool, "owner-a@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	a := New(pool, false)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/org-b/probe", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestRequireOrgBookkeeperCannotWriteContacts(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "bookkeeper@example.com", "correct-password")
	addMembership(t, pool, userID, org.ID, Bookkeeper)

	token, err := Login(ctx, pool, "bookkeeper@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	a := New(pool, false)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(PermContactsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/probe", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

// TestAuthErrorsAreJSON checks that RequireOrg's 401, 403 and 404 responses
// are all {"error":"<message>"} JSON bodies (not http.Error's plain text),
// so the SPA can parse every API error the same way.
func TestAuthErrorsAreJSON(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	orgA := createTestOrg(t, pool, "org-a")
	createTestOrg(t, pool, "org-b")
	ownerID := createTestUser(t, pool, "owner-a@example.com", "correct-password")
	addMembership(t, pool, ownerID, orgA.ID, Owner)
	bookkeeperID := createTestUser(t, pool, "bookkeeper@example.com", "correct-password")
	addMembership(t, pool, bookkeeperID, orgA.ID, Bookkeeper)

	ownerToken, err := Login(ctx, pool, "owner-a@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	bookkeeperToken, err := Login(ctx, pool, "bookkeeper@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	a := New(pool, false)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(PermContactsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	cases := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{"no session", "/api/v1/orgs/org-a/probe", "", http.StatusUnauthorized},
		{"missing permission", "/api/v1/orgs/org-a/probe", bookkeeperToken, http.StatusForbidden},
		{"not a member", "/api/v1/orgs/org-b/probe", ownerToken, http.StatusNotFound},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.token != "" {
				req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: c.token})
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, c.wantStatus)
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if body.Error == "" {
				t.Error("error body missing non-empty \"error\" key")
			}
		})
	}
}

func TestLoginHandlerSetsSecureCookieOnlyWhenConfigured(t *testing.T) {
	pool := db.TestPool(t)
	createTestUser(t, pool, "staffer@example.com", "correct-password")

	a := New(pool, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"email":"staffer@example.com","password":"correct-password"}`))
	rr := httptest.NewRecorder()
	a.LoginHandler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName {
		t.Fatalf("cookies = %+v, want one %s cookie", cookies, SessionCookieName)
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
// passed.
func TestLoginRateLimitedByEmail(t *testing.T) {
	pool := db.TestPool(t)
	createTestUser(t, pool, "ratelimited@example.com", "correct-password")

	now := time.Now()
	a := New(pool, false, WithClock(func() time.Time { return now }))

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
	pool := db.TestPool(t)

	now := time.Now()
	a := New(pool, false, WithClock(func() time.Time { return now }))

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
	pool := db.TestPool(t)
	createTestUser(t, pool, "resets-nothing@example.com", "correct-password")

	now := time.Now()
	a := New(pool, false, WithClock(func() time.Time { return now }))

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
	pool := db.TestPool(t)

	now := time.Now()
	a := New(pool, false, WithClock(func() time.Time { return now }))

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
	pool := db.TestPool(t)
	now := time.Now()
	a := New(pool, false, WithClock(func() time.Time { return now }))
	for i := 0; i < loginEmailLimit; i++ {
		rr := httptest.NewRecorder()
		a.LoginHandler(rr, loginRequest("blocked@example.com", "wrong-password"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("prime email attempt %d: %d", i, rr.Code)
		}
	}
	for i := 0; i < loginIPLimit; i++ {
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
	req := loginRequest("blocked@example.com", "wrong-password")
	req.RemoteAddr = "192.0.2.5:12345"
	a.LoginHandler(rr, req)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "900" {
		t.Fatalf("status=%d Retry-After=%q, want 429 and 900", rr.Code, rr.Header().Get("Retry-After"))
	}
}

func TestLogoutHandlerRevokesSession(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	org := createTestOrg(t, pool, "acme")
	userID := createTestUser(t, pool, "staffer@example.com", "correct-password")
	addMembership(t, pool, userID, org.ID, Owner)

	token, err := Login(ctx, pool, "staffer@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	a := New(pool, false)

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	logoutRR := httptest.NewRecorder()
	a.LogoutHandler(logoutRR, logoutReq)

	if logoutRR.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want %d, body=%s", logoutRR.Code, http.StatusNoContent, logoutRR.Body.String())
	}

	mux := http.NewServeMux()
	mux.Handle("/api/v1/orgs/{org}/probe", a.RequireOrg(PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	probeReq := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/probe", nil)
	probeReq.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	probeRR := httptest.NewRecorder()
	mux.ServeHTTP(probeRR, probeReq)

	if probeRR.Code != http.StatusUnauthorized {
		t.Errorf("status after logout = %d, want %d", probeRR.Code, http.StatusUnauthorized)
	}
}
