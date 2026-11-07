//go:build integration

// Package portal_test exercises the portal API and magic-link sign-in
// through the real HTTP handlers, so it needs to import httpapi alongside
// portal, auth, core and property without an import cycle (internal/httpapi
// itself imports internal/portal).
package portal_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/mail"
	"shigoto/internal/property"
)

const sessionCookieName = "shigoto_session"

// --- fixtures and small helpers ---

func createOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func createContact(t *testing.T, pool *pgxpool.Pool, orgID int64, kind, name, email string) core.Contact {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: kind, Name: name, Email: email})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c
}

func createDraft(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64) int64 {
	t.Helper()
	inv, err := core.CreateDraftInvoice(context.Background(), pool, orgID, core.NewInvoice{
		ContactID: contactID,
		Source:    "manual",
		DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: 1000, UnitAmount: core.Cents(totalCents), GSTCode: "N-T"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	return inv.ID
}

// createIssuedInvoice creates and issues a draft invoice for contactID,
// returning its id.
func createIssuedInvoice(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64) int64 {
	t.Helper()
	draftID := createDraft(t, pool, orgID, contactID, totalCents)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inv, err := core.IssueInvoice(ctx, tx, orgID, draftID, time.Now())
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return inv.ID
}

// createVoidInvoice creates, issues, then voids an invoice for contactID.
func createVoidInvoice(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64) int64 {
	t.Helper()
	id := createIssuedInvoice(t, pool, orgID, contactID, totalCents)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := core.VoidInvoice(ctx, tx, orgID, id); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

func createProperty(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	p, err := property.CreateProperty(context.Background(), pool, orgID, property.Property{Name: "1 Test St", Address: "1 Test St, Testville", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

func createLease(t *testing.T, pool *pgxpool.Pool, orgID, propertyID, tenantContactID int64) property.Lease {
	t.Helper()
	l, err := property.CreateLease(context.Background(), pool, orgID, property.NewLease{
		PropertyID: propertyID,
		StartDate:  time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		Frequency:  property.Weekly,
		RentAmount: 50000,
		Tenants:    []property.NewLeaseTenant{{ContactID: tenantContactID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return l
}

// createMembershipUser inserts a passwordless user (the shape portal-invite
// leaves behind) and a tenant/client membership tied to contactID.
func createMembershipUser(t *testing.T, pool *pgxpool.Pool, orgID int64, role, email string, contactID int64) int64 {
	t.Helper()
	var userID int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, NULL) RETURNING id", email, email,
	).Scan(&userID); err != nil {
		t.Fatalf("inserting portal user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role, contact_id) VALUES ($1, $2, $3, $4)",
		userID, orgID, role, contactID,
	); err != nil {
		t.Fatalf("adding portal membership: %v", err)
	}
	return userID
}

// signInPortalUser creates a passwordless user with a tenant/client
// membership tied to contactID, then signs it in via a fresh magic link
// (minted directly through auth.RequestMagicLink, bypassing email) and
// returns the resulting session token - the only way a portal user ever
// gets a session.
func signInPortalUser(t *testing.T, pool *pgxpool.Pool, h http.Handler, orgID int64, role, email string, contactID int64) string {
	t.Helper()
	createMembershipUser(t, pool, orgID, role, email, contactID)

	token, err := auth.RequestMagicLink(context.Background(), pool, email)
	if err != nil {
		t.Fatalf("RequestMagicLink: %v", err)
	}
	if token == "" {
		t.Fatalf("RequestMagicLink returned no token for eligible portal user %s", email)
	}

	rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic/verify", "", map[string]string{"token": token})
	if rr.Code != http.StatusOK {
		t.Fatalf("verify magic link status = %d, body %s", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	t.Fatalf("verify magic link response set no session cookie")
	return ""
}

func createStaffUser(t *testing.T, pool *pgxpool.Pool, email, password string, orgID int64, role string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id", email, email, hash,
	).Scan(&userID); err != nil {
		t.Fatalf("inserting staff user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)", userID, orgID, role,
	); err != nil {
		t.Fatalf("adding staff membership: %v", err)
	}
	return userID
}

func staffLogin(t *testing.T, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	token, err := auth.Login(context.Background(), pool, email, password)
	if err != nil {
		t.Fatalf("Login(%s): %v", email, err)
	}
	return token
}

// mailSink is a *mail.Mailer that records every sent message instead of
// talking SMTP, safe for concurrent Send calls: requestMagicLink (Ruling 61)
// sends from a detached goroutine, so a plain unsynchronized slice would be
// a data race under -race the moment more than one request is in flight.
type mailSink struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (s *mailSink) mailer() *mail.Mailer {
	return &mail.Mailer{From: "shigoto@example.com", Send: func(_ context.Context, m mail.Message) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sent = append(s.sent, m)
		return nil
	}}
}

func (s *mailSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *mailSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
}

func (s *mailSink) messages() []mail.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail.Message(nil), s.sent...)
}

// waitForMailCount polls sink until it holds at least want messages, failing
// the test if timeout passes first. requestMagicLink sends its email from a
// detached goroutine (Ruling 61), so a test expecting mail can't just check
// immediately after the HTTP call returns.
func waitForMailCount(t *testing.T, sink *mailSink, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if sink.count() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d sent message(s), got %d", want, sink.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestHandler(pool *pgxpool.Pool, mailer *mail.Mailer, opts ...auth.Option) http.Handler {
	a := auth.New(pool, false, opts...)
	return httpapi.New(httpapi.Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{}, BaseURL: "https://shigoto.example"},
		GetEnv: func(string) string { return "" },
		Mailer: mailer,
	})
}

func doJSON(t *testing.T, h http.Handler, method, url, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shigoto-CSRF", "1")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// insertExpiredMagicLink inserts a magic_links row that's already past its
// expiry, bypassing the 15-minute TTL auth.RequestMagicLink always applies.
func insertExpiredMagicLink(t *testing.T, pool *pgxpool.Pool, userID int64, token string) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO magic_links (token_hash, user_id, expires_at) VALUES ($1, $2, now() - interval '1 minute')",
		sum[:], userID,
	); err != nil {
		t.Fatalf("insertExpiredMagicLink: %v", err)
	}
}

// --- tests ---

func TestPortalInvoiceScoping(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	other := createOrg(t, pool, "beta")

	tenantA := createContact(t, pool, org.ID, "tenant", "Tenant A", "tenanta@example.com")
	tenantB := createContact(t, pool, org.ID, "tenant", "Tenant B", "tenantb@example.com")
	otherContact := createContact(t, pool, other.ID, "tenant", "Other Tenant", "othertenant@example.com")

	invA := createIssuedInvoice(t, pool, org.ID, tenantA.ID, 10000)
	invB := createIssuedInvoice(t, pool, org.ID, tenantB.ID, 20000)
	invOther := createIssuedInvoice(t, pool, other.ID, otherContact.ID, 30000)

	h := newTestHandler(pool, nil)
	tokenA := signInPortalUser(t, pool, h, org.ID, "tenant", "tenanta@example.com", tenantA.ID)

	rr := doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/invoices", tokenA, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rr.Code, rr.Body.String())
	}
	var invoices []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &invoices); err != nil {
		t.Fatalf("decoding invoices: %v", err)
	}
	if len(invoices) != 1 || int64(invoices[0]["id"].(float64)) != invA {
		t.Fatalf("tenant A's invoice list = %+v, want only invoice %d", invoices, invA)
	}

	// Tenant B's invoice is a 404 by id, not just excluded from the list.
	rr = doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/invoices/"+itoa(invB), tokenA, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("tenant A reading tenant B's invoice status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}

	// Another org's invoice (and that org's own slug won't even resolve a
	// membership for tokenA) is also a 404.
	rr = doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/invoices/"+itoa(invOther), tokenA, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("tenant A reading another org's invoice status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, h, http.MethodGet, "/api/v1/portal/beta/invoices", tokenA, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("tenant A listing another org's invoices status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}

	// Sanity: tenant A's own invoice detail does work.
	rr = doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/invoices/"+itoa(invA), tokenA, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("tenant A reading their own invoice status = %d, body %s", rr.Code, rr.Body.String())
	}
}

func TestPortalHidesDraftAndVoid(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	tenant := createContact(t, pool, org.ID, "tenant", "Tenant", "tenant@example.com")

	draftID := createDraft(t, pool, org.ID, tenant.ID, 5000)
	voidID := createVoidInvoice(t, pool, org.ID, tenant.ID, 6000)
	openID := createIssuedInvoice(t, pool, org.ID, tenant.ID, 7000)

	h := newTestHandler(pool, nil)
	token := signInPortalUser(t, pool, h, org.ID, "tenant", "tenant@example.com", tenant.ID)

	rr := doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/invoices", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rr.Code, rr.Body.String())
	}
	var invoices []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &invoices); err != nil {
		t.Fatalf("decoding invoices: %v", err)
	}
	if len(invoices) != 1 || int64(invoices[0]["id"].(float64)) != openID {
		t.Fatalf("portal invoice list = %+v, want only the open invoice %d", invoices, openID)
	}

	for name, id := range map[string]int64{"draft": draftID, "void": voidID} {
		rr := doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/invoices/"+itoa(id), token, nil)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s invoice detail status = %d, want 404, body %s", name, rr.Code, rr.Body.String())
		}
	}
}

func TestPortalLeasesTenantOnly(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	tenant := createContact(t, pool, org.ID, "tenant", "Tenant", "tenant@example.com")
	client := createContact(t, pool, org.ID, "client", "Client", "client@example.com")
	propertyID := createProperty(t, pool, org.ID)
	lease := createLease(t, pool, org.ID, propertyID, tenant.ID)

	h := newTestHandler(pool, nil)
	tenantToken := signInPortalUser(t, pool, h, org.ID, "tenant", "tenant@example.com", tenant.ID)
	clientToken := signInPortalUser(t, pool, h, org.ID, "client", "client@example.com", client.ID)

	rr := doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/leases", tenantToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("tenant leases status = %d, body %s", rr.Code, rr.Body.String())
	}
	var leases []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &leases); err != nil {
		t.Fatalf("decoding leases: %v", err)
	}
	if len(leases) != 1 || int64(leases[0]["id"].(float64)) != lease.ID {
		t.Fatalf("tenant leases = %+v, want only lease %d", leases, lease.ID)
	}
	if leases[0]["property_name"] != "1 Test St" {
		t.Errorf("lease property_name = %v, want '1 Test St'", leases[0]["property_name"])
	}
	if leases[0]["rent_amount"].(float64) != 50000 {
		t.Errorf("lease rent_amount = %v, want 50000", leases[0]["rent_amount"])
	}
	if leases[0]["frequency"] != "weekly" {
		t.Errorf("lease frequency = %v, want weekly", leases[0]["frequency"])
	}

	rr = doJSON(t, h, http.MethodGet, "/api/v1/portal/acme/leases", clientToken, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("client leases status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}
}

func TestMagicLinkSingleUseAndExpiry(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	tenant := createContact(t, pool, org.ID, "tenant", "Tenant", "tenant@example.com")
	userID := createMembershipUser(t, pool, org.ID, "tenant", "tenant@example.com", tenant.ID)

	h := newTestHandler(pool, nil)

	token, err := auth.RequestMagicLink(context.Background(), pool, "tenant@example.com")
	if err != nil || token == "" {
		t.Fatalf("RequestMagicLink: token=%q err=%v", token, err)
	}

	rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic/verify", "", map[string]string{"token": token})
	if rr.Code != http.StatusOK {
		t.Fatalf("first verify status = %d, body %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding verify response: %v", err)
	}
	if resp["redirect"] != "/portal" {
		t.Errorf("verify redirect = %q, want /portal", resp["redirect"])
	}

	// Single use: the same token fails the second time.
	rr = doJSON(t, h, http.MethodPost, "/api/v1/auth/magic/verify", "", map[string]string{"token": token})
	if rr.Code != http.StatusGone {
		t.Fatalf("second verify status = %d, want 410, body %s", rr.Code, rr.Body.String())
	}

	// Expiry: a magic link past its TTL also 410s.
	insertExpiredMagicLink(t, pool, userID, "expired-test-token")
	rr = doJSON(t, h, http.MethodPost, "/api/v1/auth/magic/verify", "", map[string]string{"token": "expired-test-token"})
	if rr.Code != http.StatusGone {
		t.Fatalf("expired verify status = %d, want 410, body %s", rr.Code, rr.Body.String())
	}
}

func TestMagicRequestNoEnumeration(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	tenant := createContact(t, pool, org.ID, "tenant", "Tenant", "tenant@example.com")
	createMembershipUser(t, pool, org.ID, "tenant", "tenant@example.com", tenant.ID)
	createStaffUser(t, pool, "staffonly@example.com", "correct-password", org.ID, "staff")

	sink := &mailSink{}
	h := newTestHandler(pool, sink.mailer())

	cases := []struct {
		name  string
		email string
	}{
		{"unknown email", "nosuchuser@example.com"},
		{"staff-only email", "staffonly@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink.reset()
			rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic", "", map[string]string{"email": tc.email})
			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202, body %s", rr.Code, rr.Body.String())
			}
			if strings.TrimSpace(rr.Body.String()) != "{}" {
				t.Errorf("body = %q, want {}", rr.Body.String())
			}
			// Nothing eligible means requestMagicLink never spawns the
			// send goroutine at all, so this is deterministic with no wait.
			if n := sink.count(); n != 0 {
				t.Errorf("mail sent for %s: %d message(s)", tc.name, n)
			}
		})
	}

	// The eligible portal email gets the identical response shape, but
	// mail does go out - asynchronously (Ruling 61), so wait for it rather
	// than checking immediately after the HTTP call returns.
	sink.reset()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic", "", map[string]string{"email": "tenant@example.com"})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("eligible email status = %d, want 202, body %s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "{}" {
		t.Errorf("eligible email body = %q, want {}", rr.Body.String())
	}
	waitForMailCount(t, sink, 1, 2*time.Second)
	if n := sink.count(); n != 1 {
		t.Fatalf("mail sent for eligible portal email = %d messages, want 1", n)
	}
}

func TestMagicRateLimit(t *testing.T) {
	pool := db.TestPool(t)
	h := newTestHandler(pool, (&mailSink{}).mailer())

	const email = "ratelimited@example.com"
	for i := 0; i < 5; i++ {
		rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic", "", map[string]string{"email": email})
		if rr.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d, want 202, body %s", i+1, rr.Code, rr.Body.String())
		}
	}

	rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic", "", map[string]string{"email": email})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request status = %d, want 429, body %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Errorf("429 response has no Retry-After header")
	}
}

// TestMagicLinkVerifyRejectsNewlyStaffUser covers Ruling 59 at verify time,
// not just at request time: a token issued while the user was tenant-only
// must still be rejected if the user has since gained a staff membership
// (any org) - a magic link must never yield a staff session.
func TestMagicLinkVerifyRejectsNewlyStaffUser(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	tenant := createContact(t, pool, org.ID, "tenant", "Tenant", "tenant@example.com")
	createMembershipUser(t, pool, org.ID, "tenant", "tenant@example.com", tenant.ID)

	token, err := auth.RequestMagicLink(context.Background(), pool, "tenant@example.com")
	if err != nil || token == "" {
		t.Fatalf("RequestMagicLink: token=%q err=%v", token, err)
	}

	// The user gains a staff membership (in a second org) after the token
	// was issued but before it's verified.
	other := createOrg(t, pool, "beta")
	var userID int64
	if err := pool.QueryRow(context.Background(), "SELECT id FROM users WHERE email = $1", "tenant@example.com").Scan(&userID); err != nil {
		t.Fatalf("looking up user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'staff')", userID, other.ID,
	); err != nil {
		t.Fatalf("adding staff membership: %v", err)
	}

	h := newTestHandler(pool, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/auth/magic/verify", "", map[string]string{"token": token})
	if rr.Code != http.StatusGone {
		t.Fatalf("verify status = %d, want 410, body %s", rr.Code, rr.Body.String())
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Errorf("verify response set a session cookie for a now-staff user: %+v", rr.Result().Cookies())
	}
}

func TestPortalInviteCreatesMembership(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	createStaffUser(t, pool, "owner@example.com", "correct-password", org.ID, "owner")
	ownerToken := staffLogin(t, pool, "owner@example.com", "correct-password")

	tenant := createContact(t, pool, org.ID, "tenant", "New Tenant", "newtenant@example.com")

	h := newTestHandler(pool, nil)

	rr := doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(tenant.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("portal-invite status = %d, body %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["emailed"] != false {
		t.Errorf("emailed = %v, want false (no mailer configured)", resp["emailed"])
	}
	if resp["link"] == nil || resp["link"] == "" {
		t.Errorf("link missing when emailed is false")
	}

	var role string
	var contactID int64
	if err := pool.QueryRow(context.Background(), `
		SELECT m.role, m.contact_id FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE u.email = 'newtenant@example.com' AND m.org_id = $1`, org.ID,
	).Scan(&role, &contactID); err != nil {
		t.Fatalf("querying new membership: %v", err)
	}
	if role != "tenant" || contactID != tenant.ID {
		t.Errorf("new membership role/contact_id = %s/%d, want tenant/%d", role, contactID, tenant.ID)
	}

	var auditCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log
		WHERE org_id = $1 AND entity = 'membership' AND action = 'portal_invite'`, org.ID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if auditCount < 1 {
		t.Errorf("audit rows for portal_invite = %d, want at least 1", auditCount)
	}

	// Re-inviting the same contact is idempotent.
	rr = doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(tenant.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-invite status = %d, want 200, body %s", rr.Code, rr.Body.String())
	}

	// A different contact sharing the same email conflicts (409): the
	// matching user already belongs to this org under a different contact.
	conflicting := createContact(t, pool, org.ID, "client", "Conflicting Client", "newtenant@example.com")
	rr = doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(conflicting.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("conflicting contact invite status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}

	// A contact whose email belongs to an existing staff member also
	// conflicts.
	staffContact := createContact(t, pool, org.ID, "client", "Staff Contact", "owner@example.com")
	rr = doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(staffContact.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("staff-email contact invite status = %d, want 409, body %s", rr.Code, rr.Body.String())
	}
}

func TestPortalInviteValidation(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	other := createOrg(t, pool, "beta")
	createStaffUser(t, pool, "owner@example.com", "correct-password", org.ID, "owner")
	ownerToken := staffLogin(t, pool, "owner@example.com", "correct-password")

	h := newTestHandler(pool, nil)

	supplier := createContact(t, pool, org.ID, "supplier", "A Supplier", "supplier@example.com")
	rr := doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(supplier.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("supplier contact invite status = %d, want 422, body %s", rr.Code, rr.Body.String())
	}

	noEmail := createContact(t, pool, org.ID, "tenant", "No Email", "")
	rr = doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(noEmail.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no-email contact invite status = %d, want 422, body %s", rr.Code, rr.Body.String())
	}

	otherOrgContact := createContact(t, pool, other.ID, "tenant", "Other Org Tenant", "otherorgtenant@example.com")
	rr = doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(otherOrgContact.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-org contact invite status = %d, want 404, body %s", rr.Code, rr.Body.String())
	}
}

// TestPortalInviteCrossOrgDoesNotLeakLink covers Ruling 60: a staff member
// in one org must never be handed a working magic-sign-in link for a user
// who already belongs to a different org, just by "inviting" that user's
// known email address as a brand-new contact here. Without the fix this
// let staff in org A hijack org B's tenant account outright.
func TestPortalInviteCrossOrgDoesNotLeakLink(t *testing.T) {
	pool := db.TestPool(t)
	orgA := createOrg(t, pool, "acme")
	orgB := createOrg(t, pool, "beta")
	createStaffUser(t, pool, "owner@example.com", "correct-password", orgA.ID, "owner")
	ownerToken := staffLogin(t, pool, "owner@example.com", "correct-password")

	// The victim: a genuine tenant of org B.
	victimContactB := createContact(t, pool, orgB.ID, "tenant", "Victim Tenant", "victim@example.com")
	createMembershipUser(t, pool, orgB.ID, "tenant", "victim@example.com", victimContactB.ID)

	// Staff in org A "invites" the same email address as a brand-new
	// contact of their own org, with no mailer configured.
	victimContactA := createContact(t, pool, orgA.ID, "tenant", "Same Email", "victim@example.com")
	h := newTestHandler(pool, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/orgs/acme/contacts/"+itoa(victimContactA.ID)+"/portal-invite", ownerToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("portal-invite status = %d, body %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["emailed"] != false {
		t.Errorf("emailed = %v, want false (no mailer configured)", resp["emailed"])
	}
	if _, leaked := resp["link"]; leaked {
		t.Fatalf("response leaked a magic link to another org's staff: %+v", resp)
	}
	if resp["message"] == nil || resp["message"] == "" {
		t.Errorf("response has no explanatory message when the link is withheld")
	}
}

func TestStaffEndpointsRejectPortalUser(t *testing.T) {
	pool := db.TestPool(t)
	org := createOrg(t, pool, "acme")
	tenant := createContact(t, pool, org.ID, "tenant", "Tenant", "tenant@example.com")

	h := newTestHandler(pool, nil)
	token := signInPortalUser(t, pool, h, org.ID, "tenant", "tenant@example.com", tenant.ID)

	rr := doJSON(t, h, http.MethodGet, "/api/v1/orgs/acme/invoices", token, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("tenant GET staff invoices status = %d, want 403, body %s", rr.Code, rr.Body.String())
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
