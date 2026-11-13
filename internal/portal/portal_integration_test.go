//go:build integration

// Package portal_test exercises the portal API and magic-link sign-in
// through the real HTTP handlers, so it needs to import httpapi (via
// testkit) alongside portal, auth and core without an import cycle
// (internal/httpapi itself imports internal/portal).
package portal_test

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/mail"
	"shigoto/internal/testkit"
)

// --- package-specific helpers ---

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

// sessionToken returns the session cookie r set, or "" if it set none.
func sessionToken(r testkit.Response) string {
	for _, c := range r.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c.Value
		}
	}
	return ""
}

// issueThenVoid issues a draft for contactID then voids it, returning its id.
func issueThenVoid(e *testkit.Env, org core.Org, contactID int64, totalCents int64) int64 {
	e.T.Helper()
	id := e.Invoice(org, contactID, testkit.Amount(totalCents), testkit.Issued(time.Now())).ID
	tx, err := e.Pool.Begin(e.Ctx)
	if err != nil {
		e.T.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(e.Ctx) }()
	if _, err := core.VoidInvoice(e.Ctx, tx, org.ID, id); err != nil {
		e.T.Fatalf("VoidInvoice: %v", err)
	}
	if err := tx.Commit(e.Ctx); err != nil {
		e.T.Fatalf("commit: %v", err)
	}
	return id
}

// insertExpiredMagicLink inserts a magic_links row that's already past its
// expiry, bypassing the 15-minute TTL auth.RequestMagicLink always applies.
func insertExpiredMagicLink(e *testkit.Env, userID int64, token string) {
	e.T.Helper()
	sum := sha256.Sum256([]byte(token))
	if _, err := e.Pool.Exec(e.Ctx,
		"INSERT INTO magic_links (token_hash, user_id, expires_at) VALUES ($1, $2, now() - interval '1 minute')",
		sum[:], userID,
	); err != nil {
		e.T.Fatalf("insertExpiredMagicLink: %v", err)
	}
}

// --- tests ---

func TestPortalMembershipContactScopesAcrossOrgs(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	h := e.Handler()
	var s testkit.Session
	var user testkit.User
	var firstOrgID int64
	for _, slug := range []string{"alpha", "beta"} {
		org := e.Org(slug)
		own := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Shared tenant"), testkit.Email("shared@example.com"))
		neighbor := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Private neighbor"), testkit.Email("neighbor@example.com"))
		ownInvoice := e.Invoice(org, own.ID, testkit.Amount(10000), testkit.Issued(time.Now())).ID
		otherInvoice := e.Invoice(org, neighbor.ID, testkit.Amount(20000), testkit.Issued(time.Now())).ID
		prop := e.Property(org).ID
		ownLease := e.Lease(org, prop, own.ID)
		e.Lease(org, prop, neighbor.ID)
		if s.Token == "" {
			firstOrgID = org.ID
			user = e.PortalUser("shared@example.com", org, auth.Tenant, own.ID)
			s = e.Login(user, h)
		} else {
			if _, err := pool.Exec(ctx, "INSERT INTO memberships (user_id,org_id,role,contact_id) VALUES ($1,$2,'tenant',$3)", user.ID, org.ID, own.ID); err != nil {
				t.Fatal(err)
			}
		}
		base := "/api/v1/portal/" + slug
		for path, wantID := range map[string]int64{"/invoices": ownInvoice, "/leases": ownLease.ID} {
			rr := s.Do("GET", base+path, nil)
			var rows []struct {
				ID int64 `json:"id"`
			}
			if rr.Code != 200 {
				t.Fatalf("%s: %d %s", base+path, rr.Code, rr.Body)
			}
			rr.JSON(&rows)
			if len(rows) != 1 || rows[0].ID != wantID {
				t.Fatalf("%s exposed wrong contact: %+v", base+path, rows)
			}
		}
		for id, want := range map[int64]int{ownInvoice: 200, otherInvoice: 404} {
			rr := s.Do("GET", base+"/invoices/"+strconv.FormatInt(id, 10), nil)
			if rr.Code != want {
				t.Fatalf("invoice %d: %d want %d", id, rr.Code, want)
			}
			if want == 404 && strings.TrimSpace(string(rr.Body)) != `{"error":"not found"}` {
				t.Fatal("foreign contact detail leaked")
			}
		}
	}
	if _, err := pool.Exec(ctx, "DELETE FROM memberships WHERE user_id=$1 AND org_id=$2", user.ID, firstOrgID); err != nil {
		t.Fatal(err)
	}
	for slug, want := range map[string]int{"alpha": 404, "beta": 200} {
		if rr := s.Do("GET", "/api/v1/portal/"+slug+"/invoices", nil); rr.Code != want {
			t.Fatalf("after revoke %s: %d want %d", slug, rr.Code, want)
		}
	}
	sum := sha256.Sum256([]byte(s.Token))
	if _, err := pool.Exec(ctx, "UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=$1", sum[:]); err != nil {
		t.Fatal(err)
	}
	if rr := s.Do("GET", "/api/v1/portal/beta/invoices", nil); rr.Code != 401 {
		t.Fatalf("expired session: %d want 401", rr.Code)
	}
}

// TestPortalHidesVoid covers what the portal.yaml cases can't seed: a void
// invoice is neither listed nor readable by its own contact (drafts are
// covered in portal.yaml).
func TestPortalHidesVoid(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"), testkit.Email("tenant@example.com"))

	voidID := issueThenVoid(e, org, tenant.ID, 6000)
	openID := e.Invoice(org, tenant.ID, testkit.Amount(7000), testkit.Issued(time.Now())).ID

	h := e.Handler()
	s := e.Login(e.PortalUser("tenant@example.com", org, auth.Tenant, tenant.ID), h)

	rr := s.Do(http.MethodGet, "/api/v1/portal/acme/invoices", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rr.Code, rr.Body)
	}
	var invoices []map[string]any
	rr.JSON(&invoices)
	if len(invoices) != 1 || int64(invoices[0]["id"].(float64)) != openID {
		t.Fatalf("portal invoice list = %+v, want only the open invoice %d", invoices, openID)
	}

	rr = s.Do(http.MethodGet, "/api/v1/portal/acme/invoices/"+strconv.FormatInt(voidID, 10), nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("void invoice detail status = %d, want 404, body %s", rr.Code, rr.Body)
	}
}

func TestMagicLinkSingleUseAndExpiry(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"), testkit.Email("tenant@example.com"))
	user := e.PortalUser("tenant@example.com", org, auth.Tenant, tenant.ID)

	anon := e.Anonymous(e.Handler())

	token, err := auth.RequestMagicLink(e.Ctx, e.Pool, "tenant@example.com")
	if err != nil || token == "" {
		t.Fatalf("RequestMagicLink: token=%q err=%v", token, err)
	}

	rr := anon.Do(http.MethodPost, "/api/v1/auth/magic/verify", map[string]string{"token": token})
	if rr.Code != http.StatusOK {
		t.Fatalf("first verify status = %d, body %s", rr.Code, rr.Body)
	}
	var resp map[string]string
	rr.JSON(&resp)
	if resp["redirect"] != "/portal" {
		t.Errorf("verify redirect = %q, want /portal", resp["redirect"])
	}

	// Single use: the same token fails the second time.
	rr = anon.Do(http.MethodPost, "/api/v1/auth/magic/verify", map[string]string{"token": token})
	if rr.Code != http.StatusGone {
		t.Fatalf("second verify status = %d, want 410, body %s", rr.Code, rr.Body)
	}

	// Expiry: a magic link past its TTL also 410s.
	insertExpiredMagicLink(e, user.ID, "expired-test-token")
	rr = anon.Do(http.MethodPost, "/api/v1/auth/magic/verify", map[string]string{"token": "expired-test-token"})
	if rr.Code != http.StatusGone {
		t.Fatalf("expired verify status = %d, want 410, body %s", rr.Code, rr.Body)
	}
}

func TestMagicRequestNoEnumeration(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"), testkit.Email("tenant@example.com"))
	e.PortalUser("tenant@example.com", org, auth.Tenant, tenant.ID)
	e.Member(e.User("staffonly@example.com", testkit.WithPassword("correct-password")), org, auth.Staff)

	sink := &mailSink{}
	h := e.Handler(testkit.Mailer(sink.mailer()))

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
			child := *e
			child.T = t
			rr := child.Anonymous(h).Do(http.MethodPost, "/api/v1/auth/magic", map[string]string{"email": tc.email})
			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202, body %s", rr.Code, rr.Body)
			}
			if strings.TrimSpace(string(rr.Body)) != "{}" {
				t.Errorf("body = %q, want {}", rr.Body)
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
	rr := e.Anonymous(h).Do(http.MethodPost, "/api/v1/auth/magic", map[string]string{"email": "tenant@example.com"})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("eligible email status = %d, want 202, body %s", rr.Code, rr.Body)
	}
	if strings.TrimSpace(string(rr.Body)) != "{}" {
		t.Errorf("eligible email body = %q, want {}", rr.Body)
	}
	waitForMailCount(t, sink, 1, 2*time.Second)
	if n := sink.count(); n != 1 {
		t.Fatalf("mail sent for eligible portal email = %d messages, want 1", n)
	}
}

func TestMagicRateLimit(t *testing.T) {
	e := testkit.New(t)
	anon := e.Anonymous(e.Handler(testkit.Mailer((&mailSink{}).mailer())))

	const email = "ratelimited@example.com"
	for i := 0; i < 5; i++ {
		rr := anon.Do(http.MethodPost, "/api/v1/auth/magic", map[string]string{"email": email})
		if rr.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d, want 202, body %s", i+1, rr.Code, rr.Body)
		}
	}

	rr := anon.Do(http.MethodPost, "/api/v1/auth/magic", map[string]string{"email": email})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request status = %d, want 429, body %s", rr.Code, rr.Body)
	}
	if rr.Header.Get("Retry-After") == "" {
		t.Errorf("429 response has no Retry-After header")
	}
}

// TestMagicLinkVerifyRejectsNewlyStaffUser covers Ruling 59 at verify time,
// not just at request time: a token issued while the user was tenant-only
// must still be rejected if the user has since gained a staff membership
// (any org) - a magic link must never yield a staff session.
func TestMagicLinkVerifyRejectsNewlyStaffUser(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"), testkit.Email("tenant@example.com"))
	user := e.PortalUser("tenant@example.com", org, auth.Tenant, tenant.ID)

	token, err := auth.RequestMagicLink(e.Ctx, e.Pool, "tenant@example.com")
	if err != nil || token == "" {
		t.Fatalf("RequestMagicLink: token=%q err=%v", token, err)
	}

	// The user gains a staff membership (in a second org) after the token
	// was issued but before it's verified.
	e.Member(user, e.Org("beta"), auth.Staff)

	rr := e.Anonymous(e.Handler()).Do(http.MethodPost, "/api/v1/auth/magic/verify", map[string]string{"token": token})
	if rr.Code != http.StatusGone {
		t.Fatalf("verify status = %d, want 410, body %s", rr.Code, rr.Body)
	}
	if len(rr.Cookies()) != 0 {
		t.Errorf("verify response set a session cookie for a now-staff user: %+v", rr.Cookies())
	}
}

func TestPortalInviteCreatesMembership(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	owner := e.User("owner@example.com", testkit.WithPassword("correct-password"))
	e.Member(owner, org, auth.Owner)

	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("New Tenant"), testkit.Email("newtenant@example.com"))

	s := e.Login(owner, e.Handler())

	rr := s.Do(http.MethodPost, "/api/v1/orgs/acme/contacts/"+strconv.FormatInt(tenant.ID, 10)+"/portal-invite", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("portal-invite status = %d, body %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	rr.JSON(&resp)
	if resp["emailed"] != false {
		t.Errorf("emailed = %v, want false (no mailer configured)", resp["emailed"])
	}
	if resp["link"] == nil || resp["link"] == "" {
		t.Errorf("link missing when emailed is false")
	}

	var role string
	var contactID int64
	if err := pool.QueryRow(ctx, `
		SELECT m.role, m.contact_id FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE u.email = 'newtenant@example.com' AND m.org_id = $1`, org.ID,
	).Scan(&role, &contactID); err != nil {
		t.Fatalf("querying new membership: %v", err)
	}
	if role != "tenant" || contactID != tenant.ID {
		t.Errorf("new membership role/contact_id = %s/%d, want tenant/%d", role, contactID, tenant.ID)
	}

	if auditCount := e.Count("audit_log", "org_id = $1 AND entity = 'membership' AND action = 'portal_invite'", org.ID); auditCount < 1 {
		t.Errorf("audit rows for portal_invite = %d, want at least 1", auditCount)
	}

	// Re-inviting the same contact is idempotent.
	rr = s.Do(http.MethodPost, "/api/v1/orgs/acme/contacts/"+strconv.FormatInt(tenant.ID, 10)+"/portal-invite", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-invite status = %d, want 200, body %s", rr.Code, rr.Body)
	}

	// A different contact sharing the same email conflicts (409): the
	// matching user already belongs to this org under a different contact.
	conflicting := e.Contact(org, testkit.Kind("client"), testkit.Name("Conflicting Client"), testkit.Email("newtenant@example.com"))
	rr = s.Do(http.MethodPost, "/api/v1/orgs/acme/contacts/"+strconv.FormatInt(conflicting.ID, 10)+"/portal-invite", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("conflicting contact invite status = %d, want 409, body %s", rr.Code, rr.Body)
	}

	// A contact whose email belongs to an existing staff member also
	// conflicts.
	staffContact := e.Contact(org, testkit.Kind("client"), testkit.Name("Staff Contact"), testkit.Email("owner@example.com"))
	rr = s.Do(http.MethodPost, "/api/v1/orgs/acme/contacts/"+strconv.FormatInt(staffContact.ID, 10)+"/portal-invite", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("staff-email contact invite status = %d, want 409, body %s", rr.Code, rr.Body)
	}
}

// TestStaffInviteAcceptByPasswordlessUserRequiresPassword covers Ruling 63:
// a portal-only user (created via magic link, so password_hash is NULL)
// accepting a staff invite with OIDC disabled must supply a password, and
// can log in with it afterwards.
func TestStaffInviteAcceptByPasswordlessUserRequiresPassword(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	// A second org: CreateInvite rejects inviting an email already a member
	// of the target org (any role), so the staff invite below targets an
	// org the portal user doesn't already belong to.
	staffOrg := e.Org("beta")
	owner := e.User("owner@example.com", testkit.WithPassword("correct-password"))
	e.Member(owner, staffOrg, auth.Owner)

	h := e.Handler()

	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"), testkit.Email("tenant@example.com"))
	portal := e.Login(e.PortalUser("tenant@example.com", org, auth.Tenant, tenant.ID), h)

	_, inviteToken, err := auth.CreateInvite(e.Ctx, e.Pool, staffOrg.ID, "tenant@example.com", "staff", owner.ID)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	// GET invite details while signed in as the passwordless user: OIDC is
	// disabled on this handler, so needs_password must be true.
	rr := portal.Do(http.MethodGet, "/api/v1/invites/"+inviteToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET invite details status = %d, body %s", rr.Code, rr.Body)
	}
	var details map[string]any
	rr.JSON(&details)
	if details["needs_password"] != true {
		t.Errorf("needs_password = %v, want true for a passwordless user with OIDC disabled", details["needs_password"])
	}

	// Accepting without a password fails.
	rr = portal.Do(http.MethodPost, "/api/v1/invites/"+inviteToken+"/accept", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("accept without password status = %d, want 422, body %s", rr.Code, rr.Body)
	}

	// Accepting with a valid password succeeds, in the same transaction
	// (the earlier failed attempt rolled back the token consumption).
	rr = portal.Do(http.MethodPost, "/api/v1/invites/"+inviteToken+"/accept",
		map[string]string{"password": "a-very-long-password"})
	if rr.Code != http.StatusOK {
		t.Fatalf("accept with password status = %d, body %s", rr.Code, rr.Body)
	}

	// The password now works for ordinary password login.
	if _, err := auth.Login(e.Ctx, e.Pool, "tenant@example.com", "a-very-long-password"); err != nil {
		t.Fatalf("Login after accepting with a password: %v", err)
	}
}

// TestAcceptInviteRevokesOtherSessions covers Ruling 64 (security): session
// S1 and S2 (a portal magic-link session) both exist for the same
// passwordless user; accepting a staff invite as S2 must kill S1 and start
// a fresh replacement session for S2's request.
func TestAcceptInviteRevokesOtherSessions(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	// A second org: CreateInvite rejects inviting an email already a member
	// of the target org (any role), so the staff invite below targets an
	// org the portal user doesn't already belong to.
	staffOrg := e.Org("beta")
	owner := e.User("owner@example.com", testkit.WithPassword("correct-password"))
	e.Member(owner, staffOrg, auth.Owner)

	h := e.Handler()

	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tenant"), testkit.Email("tenant@example.com"))
	s1 := e.Login(e.PortalUser("tenant@example.com", org, auth.Tenant, tenant.ID), h)

	magicToken2, err := auth.RequestMagicLink(e.Ctx, e.Pool, "tenant@example.com")
	if err != nil || magicToken2 == "" {
		t.Fatalf("RequestMagicLink for second session: token=%q err=%v", magicToken2, err)
	}
	rr := e.Anonymous(h).Do(http.MethodPost, "/api/v1/auth/magic/verify", map[string]string{"token": magicToken2})
	if rr.Code != http.StatusOK {
		t.Fatalf("verify second magic link status = %d, body %s", rr.Code, rr.Body)
	}
	s2 := sessionToken(rr)
	if s2 == "" {
		t.Fatalf("second magic link verify set no session cookie")
	}

	// Sanity: both sessions work before the invite is accepted.
	if rr := s1.Do(http.MethodGet, "/api/v1/me", nil); rr.Code != http.StatusOK {
		t.Fatalf("s1 GET /me before accept status = %d, body %s", rr.Code, rr.Body)
	}

	_, inviteToken, err := auth.CreateInvite(e.Ctx, e.Pool, staffOrg.ID, "tenant@example.com", "staff", owner.ID)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	rr = e.Resume(s2, h).Do(http.MethodPost, "/api/v1/invites/"+inviteToken+"/accept",
		map[string]string{"password": "a-very-long-password"})
	if rr.Code != http.StatusOK {
		t.Fatalf("accept via s2 status = %d, body %s", rr.Code, rr.Body)
	}
	s2New := sessionToken(rr)
	if s2New == "" {
		t.Fatalf("accept response set no replacement session cookie")
	}

	if rr := s1.Do(http.MethodGet, "/api/v1/me", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("s1 GET /me after accept status = %d, want 401 (revoked)", rr.Code)
	}
	if rr := e.Resume(s2New, h).Do(http.MethodGet, "/api/v1/me", nil); rr.Code != http.StatusOK {
		t.Fatalf("s2's replacement session GET /me status = %d, want 200, body %s", rr.Code, rr.Body)
	}
}
