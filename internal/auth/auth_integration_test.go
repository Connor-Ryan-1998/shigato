//go:build integration

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
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
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
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
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
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
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("cookies = %+v, want one %s cookie", cookies, sessionCookieName)
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
