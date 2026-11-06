//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/payments"
)

// sessionCookieName mirrors the unexported constant of the same name in
// package auth (internal/auth/session.go); there's no exported way to get
// it from here.
const sessionCookieName = "shigoto_session"

const webhookSecret = "whsec_test_secret"

func webhookTestOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func webhookTestContact(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: "client", Name: "Test Contact"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func webhookTestUser(t *testing.T, pool *pgxpool.Pool, email, password string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
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

func webhookTestAddMembership(t *testing.T, pool *pgxpool.Pool, userID, orgID int64, role auth.Role) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, orgID, string(role),
	)
	if err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

// createDraft creates a one-line draft invoice whose line totals to exactly
// totalCents (GST-free), with the given source and a 14-day term.
func webhookTestDraft(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, source string, totalCents int64) int64 {
	t.Helper()
	inv, err := core.CreateDraftInvoice(context.Background(), pool, orgID, core.NewInvoice{
		ContactID: contactID,
		Source:    source,
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

func newWebhookHandler(pool *pgxpool.Pool, fakes map[string]*payments.Fake) http.Handler {
	a := auth.New(pool, false)
	return New(Deps{
		Pool: pool,
		Auth: a,
		Cfg:  config.Config{Modules: map[string]bool{}},
		Payments: func(orgSlug string) (payments.Provider, error) {
			f, ok := fakes[orgSlug]
			if !ok {
				return nil, payments.ErrNoProvider
			}
			return f, nil
		},
	})
}

// webhookContactAndInvoice creates a contact and an issued invoice with
// provider_ref already set to providerRef, as if the issue route had
// already sent it to the provider.
func webhookContactAndInvoice(t *testing.T, pool *pgxpool.Pool, orgID int64, providerRef string, totalCents int64) int64 {
	t.Helper()
	ctx := context.Background()
	contactID := webhookTestContact(t, pool, orgID)
	draftID := webhookTestDraft(t, pool, orgID, contactID, "manual", totalCents)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	inv, err := core.IssueInvoice(ctx, tx, orgID, draftID, time.Now())
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if _, err := pool.Exec(ctx, "UPDATE invoices SET provider_ref = $1 WHERE org_id = $2 AND id = $3", providerRef, orgID, inv.ID); err != nil {
		t.Fatalf("setting provider_ref: %v", err)
	}
	return inv.ID
}

// fakeWebhookBody builds a JSON body in the shape payments.Fake.ParseWebhook
// decodes.
func fakeWebhookBody(t *testing.T, id, kind, invoiceRef, paymentRef string, amount int64, paidOn time.Time) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":          id,
		"kind":        kind,
		"invoice_ref": invoiceRef,
		"payment_ref": paymentRef,
		"amount":      amount,
		"paid_on":     paidOn.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshaling webhook body: %v", err)
	}
	return body
}

func postWebhook(handler http.Handler, slug string, body []byte, sigHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe/"+slug, bytes.NewReader(body))
	if sigHeader != "" {
		req.Header.Set("Stripe-Signature", sigHeader)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestWebhookPaysInvoice(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_1", 10000)
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	body := fakeWebhookBody(t, "evt_1", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	rr := postWebhook(handler, "acme", body, fake.Sign(body, time.Now()))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	inv, err := core.GetInvoice(context.Background(), pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "paid" {
		t.Fatalf("status = %s, want paid", inv.Status)
	}

	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM payments WHERE org_id=$1 AND invoice_id=$2", org.ID, invID).Scan(&count); err != nil {
		t.Fatalf("counting payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("payment rows = %d, want 1", count)
	}
}

func TestWebhookDuplicateDelivery(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_1", 10000)
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	body := fakeWebhookBody(t, "evt_dup", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	sig := fake.Sign(body, time.Now())

	// Sequential redelivery.
	if rr := postWebhook(handler, "acme", body, sig); rr.Code != http.StatusOK {
		t.Fatalf("first delivery: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if rr := postWebhook(handler, "acme", body, sig); rr.Code != http.StatusOK {
		t.Fatalf("second (sequential) delivery: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	// Concurrent redelivery of the same event.
	const n = 5
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = postWebhook(handler, "acme", body, sig).Code
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent delivery %d: status = %d, want 200", i, code)
		}
	}

	var eventCount, paymentCount int
	ctx := context.Background()
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM webhook_events WHERE provider='stripe' AND event_id=$1", "evt_dup").Scan(&eventCount); err != nil {
		t.Fatalf("counting webhook_events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("webhook_events rows = %d, want 1", eventCount)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM payments WHERE org_id=$1 AND invoice_id=$2", org.ID, invID).Scan(&paymentCount); err != nil {
		t.Fatalf("counting payments: %v", err)
	}
	if paymentCount != 1 {
		t.Fatalf("payment rows = %d, want 1", paymentCount)
	}
}

func TestWebhookBadSignature400NoChange(t *testing.T) {
	pool := db.TestPool(t)
	org := webhookTestOrg(t, pool, "acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_1", 10000)
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	body := fakeWebhookBody(t, "evt_bad", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	badSig := (&payments.Fake{Secret: "wrong_secret"}).Sign(body, time.Now())

	rr := postWebhook(handler, "acme", body, badSig)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}

	ctx := context.Background()
	inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (unchanged)", inv.Status)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM webhook_events").Scan(&count); err != nil {
		t.Fatalf("counting webhook_events: %v", err)
	}
	if count != 0 {
		t.Fatalf("webhook_events rows = %d, want 0", count)
	}
}

func TestWebhookUnknownOrg404(t *testing.T) {
	pool := db.TestPool(t)
	handler := newWebhookHandler(pool, map[string]*payments.Fake{})

	body := fakeWebhookBody(t, "evt_1", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	rr := postWebhook(handler, "no-such-org", body, "t=1,v1=deadbeef")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestIssueSendFailureKeepsNumberAndRetries(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	contactID := webhookTestContact(t, pool, org.ID)
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	// The first send fails part-way, after one of the two invoice items
	// has been created provider-side.
	fake := &payments.Fake{Secret: webhookSecret, SendInvoiceErr: fmt.Errorf("stripe is down"), SendInvoiceErrAfterItems: 1}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	draft, err := core.CreateDraftInvoice(ctx, pool, org.ID, core.NewInvoice{
		ContactID: contactID, Source: "manual", DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "a", QuantityMilli: 1000, UnitAmount: 1000, GSTCode: "N-T"},
			{Description: "b", QuantityMilli: 1000, UnitAmount: 2000, GSTCode: "N-T"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	draftID := draft.ID

	issueReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	issueReq.Header.Set("X-Shigoto-CSRF", "1")
	issueReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, issueReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" || inv.Number == nil || *inv.Number != 1 {
		t.Fatalf("after failed send: status=%s number=%v, want open, 1", inv.Status, inv.Number)
	}
	if inv.ProviderRef != "" {
		t.Fatalf("after failed send: provider_ref = %q, want empty", inv.ProviderRef)
	}

	sendReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", draftID), nil)
	sendReq.Header.Set("X-Shigoto-CSRF", "1")
	sendReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, sendReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("send retry: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	inv, err = core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" || inv.Number == nil || *inv.Number != 1 {
		t.Fatalf("after retry: status=%s number=%v, want open, 1", inv.Status, inv.Number)
	}
	if inv.ProviderRef == "" {
		t.Fatalf("after retry: provider_ref is empty, want set")
	}
	// The retry must replay onto the provider invoice and items the failed
	// attempt already created, not add the first item a second time.
	if n := fake.ItemCount(inv.ProviderRef); n != 2 {
		t.Fatalf("provider invoice items = %d, want 2 (one per line, no duplicates)", n)
	}
	if n := len(fake.Customers()); n != 2 {
		t.Fatalf("EnsureCustomer calls = %d, want 2 (retried after rollback)", n)
	}
	if refs := fake.CustomerRefs(); len(refs) != 1 {
		t.Fatalf("provider customers = %v, want exactly 1 (no duplicate customer)", refs)
	}
}

func TestSendAlreadySentRejected(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	contactID := webhookTestContact(t, pool, org.ID)
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	draftID := webhookTestDraft(t, pool, org.ID, contactID, "manual", 1000)

	issueReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	issueReq.Header.Set("X-Shigoto-CSRF", "1")
	issueReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, issueReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	// The fake has no SendInvoiceErr, so issuing already sent successfully.
	// A /send retry on an already-sent invoice must be rejected rather
	// than sending a second time.
	sendReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", draftID), nil)
	sendReq.Header.Set("X-Shigoto-CSRF", "1")
	sendReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, sendReq)
	if rr.Code != http.StatusConflict {
		t.Fatalf("resend: status = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}

	if calls := fake.Calls(); len(calls) != 1 {
		t.Fatalf("fake SendInvoice calls = %d, want 1 (no double-send)", len(calls))
	}
}

// TestSendPastDueRejected covers finding 6: billing refuses to send an
// invoice whose due date has passed before touching the provider at all
// (no EnsureCustomer), and the route maps that to 409.
func TestSendPastDueRejected(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	contactID := webhookTestContact(t, pool, org.ID)
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})
	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Issued 30 days ago on a 14-day term: due 16 days ago, never sent.
	draftID := webhookTestDraft(t, pool, org.ID, contactID, "manual", 1000)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := core.IssueInvoice(ctx, tx, org.ID, draftID, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	sendReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", draftID), nil)
	sendReq.Header.Set("X-Shigoto-CSRF", "1")
	sendReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, sendReq)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "due date has passed; void and reissue") {
		t.Fatalf("send: status = %d body=%s, want 409 with 'due date has passed; void and reissue'", rr.Code, rr.Body.String())
	}
	if n := len(fake.Customers()); n != 0 {
		t.Fatalf("EnsureCustomer calls = %d, want 0", n)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Fatalf("SendInvoice calls = %d, want 0", n)
	}
}

// TestSendUnknownOrCrossOrgInvoice404 covers POST .../send for an invoice
// id that doesn't exist, and for one that belongs to another org: both
// must 404 (core.ErrNotFound from billing.Send's GetInvoice), not 500.
func TestSendUnknownOrCrossOrgInvoice404(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	other := webhookTestOrg(t, pool, "other")
	otherContact := webhookTestContact(t, pool, other.ID)
	otherDraftID := webhookTestDraft(t, pool, other.ID, otherContact, "manual", 1000)

	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	post := func(id int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", id), nil)
		req.Header.Set("X-Shigoto-CSRF", "1")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	if rr := post(999999); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown invoice id: status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
	if rr := post(otherDraftID); rr.Code != http.StatusNotFound {
		t.Fatalf("cross-org invoice id: status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestVoidCallsProviderVoidInvoice(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	contactID := webhookTestContact(t, pool, org.ID)
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	draftID := webhookTestDraft(t, pool, org.ID, contactID, "manual", 1000)

	issueReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	issueReq.Header.Set("X-Shigoto-CSRF", "1")
	issueReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, issueReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.ProviderRef == "" {
		t.Fatalf("precondition: provider_ref is empty, want set after a successful send")
	}

	voidReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/void", draftID), nil)
	voidReq.Header.Set("X-Shigoto-CSRF", "1")
	voidReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, voidReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("void: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	voided := fake.VoidedRefs()
	if len(voided) != 1 || voided[0] != inv.ProviderRef {
		t.Fatalf("fake voided = %v, want [%s]", voided, inv.ProviderRef)
	}

	inv, err = core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "void" {
		t.Fatalf("status = %s, want void", inv.Status)
	}
}

// TestVoidRefusesInvoiceWithPayments covers the follow-ups void guard: an
// invoice with any recorded payment (even a partial one) must be refused
// with 409 rather than voided, since shigoto has no credit-note/refund
// path yet to reconcile the money already collected.
func TestVoidRefusesInvoiceWithPayments(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	contactID := webhookTestContact(t, pool, org.ID)
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	handler := newWebhookHandler(pool, nil)
	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	draftID := webhookTestDraft(t, pool, org.ID, contactID, "manual", 10000)
	issueReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	issueReq.Header.Set("X-Shigoto-CSRF", "1")
	issueReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, issueReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	// A partial payment, not the full amount: it still counts as "has
	// payments" for the void guard.
	payBody, err := json.Marshal(map[string]any{"amount": 4000, "paid_on": "2026-11-05", "method": "cash"})
	if err != nil {
		t.Fatalf("marshaling payment body: %v", err)
	}
	payReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/payments", draftID), bytes.NewReader(payBody))
	payReq.Header.Set("X-Shigoto-CSRF", "1")
	payReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	payReq.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, payReq)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("record payment: status = %d, want 200/201, body=%s", rr.Code, rr.Body.String())
	}

	voidReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/void", draftID), nil)
	voidReq.Header.Set("X-Shigoto-CSRF", "1")
	voidReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, voidReq)
	if rr.Code != http.StatusConflict {
		t.Fatalf("void: status = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}
	if errBody.Error != "invoice has payments; record a credit note instead" {
		t.Fatalf("error = %q, want %q", errBody.Error, "invoice has payments; record a credit note instead")
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (refused void must not change it)", inv.Status)
	}
}

func TestVoidProviderFailureLeavesOpen(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	contactID := webhookTestContact(t, pool, org.ID)
	userID := webhookTestUser(t, pool, "owner@example.com", "correct-password")
	webhookTestAddMembership(t, pool, userID, org.ID, auth.Owner)

	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	token, err := auth.Login(ctx, pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	draftID := webhookTestDraft(t, pool, org.ID, contactID, "manual", 1000)

	issueReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	issueReq.Header.Set("X-Shigoto-CSRF", "1")
	issueReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, issueReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	fake.VoidInvoiceErr = fmt.Errorf("stripe is down")

	voidReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/void", draftID), nil)
	voidReq.Header.Set("X-Shigoto-CSRF", "1")
	voidReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, voidReq)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("void: status = %d, want 502, body=%s", rr.Code, rr.Body.String())
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (the DB void must roll back with the failed provider void)", inv.Status)
	}
}

// auditCount counts audit_log rows for org with the given action.
func auditCount(t *testing.T, pool *pgxpool.Pool, orgID int64, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_log WHERE org_id=$1 AND action=$2 AND actor_user_id IS NULL", orgID, action,
	).Scan(&n); err != nil {
		t.Fatalf("counting audit_log %s: %v", action, err)
	}
	return n
}

func paymentCount(t *testing.T, pool *pgxpool.Pool, orgID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM payments WHERE org_id=$1", orgID).Scan(&n); err != nil {
		t.Fatalf("counting payments: %v", err)
	}
	return n
}

// metadataWebhookBody is fakeWebhookBody plus a shigoto_invoice_id, as if
// the provider echoed back the invoice's metadata.
func metadataWebhookBody(t *testing.T, id, invoiceRef string, shigotoInvoiceID, amount int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "kind": "invoice.paid", "invoice_ref": invoiceRef, "payment_ref": "pi_" + id,
		"amount": amount, "paid_on": time.Now().Format(time.RFC3339), "shigoto_invoice_id": shigotoInvoiceID,
	})
	if err != nil {
		t.Fatalf("marshaling webhook body: %v", err)
	}
	return body
}

// TestWebhookNonRetryableRejected covers invoice.paid events that can never
// succeed (a zero amount; a payment against a void invoice): they must be
// audited as payment_rejected and acknowledged with 200, not 500, which
// would make Stripe retry them for days.
func TestWebhookNonRetryableRejected(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_1", 10000)
	voidID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_void", 10000)
	if _, err := pool.Exec(ctx, "UPDATE invoices SET status='void' WHERE id=$1", voidID); err != nil {
		t.Fatalf("voiding: %v", err)
	}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	zero := fakeWebhookBody(t, "evt_zero", "invoice.paid", "in_fake_1", "pi_zero", 0, time.Now())
	if rr := postWebhook(handler, "acme", zero, fake.Sign(zero, time.Now())); rr.Code != http.StatusOK {
		t.Fatalf("zero amount: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	void := fakeWebhookBody(t, "evt_void", "invoice.paid", "in_fake_void", "pi_void", 10000, time.Now())
	if rr := postWebhook(handler, "acme", void, fake.Sign(void, time.Now())); rr.Code != http.StatusOK {
		t.Fatalf("void invoice: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	if n := paymentCount(t, pool, org.ID); n != 0 {
		t.Fatalf("payments = %d, want 0", n)
	}
	if n := auditCount(t, pool, org.ID, "payment_rejected"); n != 2 {
		t.Fatalf("payment_rejected audit rows = %d, want 2", n)
	}
	var events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM webhook_events WHERE org_id=$1", org.ID).Scan(&events); err != nil {
		t.Fatalf("counting webhook_events: %v", err)
	}
	if events != 2 {
		t.Fatalf("webhook_events = %d, want 2 (both committed so redelivery is a no-op)", events)
	}
	inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open", inv.Status)
	}
}

// TestWebhookOrphanEvent covers an invoice.paid event whose provider ref
// matches no invoice and whose metadata names no invoice in the URL's org
// (here: another org's invoice): audited as orphan_event, 200, and the
// other org's invoice is untouched.
func TestWebhookOrphanEvent(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	other := webhookTestOrg(t, pool, "other")
	otherInv := webhookContactAndInvoice(t, pool, other.ID, "in_other", 10000)
	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	for _, body := range [][]byte{
		fakeWebhookBody(t, "evt_orphan", "invoice.paid", "in_unknown", "pi_orphan", 10000, time.Now()),
		metadataWebhookBody(t, "evt_cross", "in_unknown_2", otherInv, 10000),
	} {
		if rr := postWebhook(handler, "acme", body, fake.Sign(body, time.Now())); rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
	}

	if n := auditCount(t, pool, org.ID, "orphan_event"); n != 2 {
		t.Fatalf("orphan_event audit rows = %d, want 2", n)
	}
	if n := paymentCount(t, pool, other.ID); n != 0 {
		t.Fatalf("other org payments = %d, want 0", n)
	}
	inv, err := core.GetInvoice(ctx, pool, other.ID, otherInv)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("other org invoice status = %s, want open", inv.Status)
	}
}

// TestWebhookMetadataFallback covers Stripe accepting a send whose
// provider_ref shigoto never committed: the paid event's InvoiceRef
// matches nothing, so the payment is matched by metadata.shigoto_invoice_id.
func TestWebhookMetadataFallback(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	invID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_1", 10000)
	if _, err := pool.Exec(ctx, "UPDATE invoices SET provider_ref=NULL WHERE id=$1", invID); err != nil {
		t.Fatalf("clearing provider_ref: %v", err)
	}
	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	body := metadataWebhookBody(t, "evt_meta", "in_never_stored", invID, 10000)
	if rr := postWebhook(handler, "acme", body, fake.Sign(body, time.Now())); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "paid" {
		t.Fatalf("status = %s, want paid", inv.Status)
	}
	if n := auditCount(t, pool, org.ID, "orphan_event"); n != 0 {
		t.Fatalf("orphan_event audit rows = %d, want 0", n)
	}
}

// TestWebhookMetadataFallbackBackfillsRef covers the same metadata-fallback
// match as TestWebhookMetadataFallback, but additionally checks that the
// never-committed provider_ref is backfilled from the event, and that the
// recorded_by_webhook audit diff records how the invoice was matched.
func TestWebhookMetadataFallbackBackfillsRef(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := webhookTestOrg(t, pool, "acme")
	invID := webhookContactAndInvoice(t, pool, org.ID, "in_fake_1", 10000)
	if _, err := pool.Exec(ctx, "UPDATE invoices SET provider_ref=NULL WHERE id=$1", invID); err != nil {
		t.Fatalf("clearing provider_ref: %v", err)
	}
	fake := &payments.Fake{Secret: webhookSecret}
	handler := newWebhookHandler(pool, map[string]*payments.Fake{"acme": fake})

	const wantRef = "in_never_stored"
	body := metadataWebhookBody(t, "evt_meta_backfill", wantRef, invID, 10000)
	if rr := postWebhook(handler, "acme", body, fake.Sign(body, time.Now())); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.ProviderRef != wantRef {
		t.Fatalf("provider_ref = %q, want %q (backfilled from the event)", inv.ProviderRef, wantRef)
	}

	var diff []byte
	if err := pool.QueryRow(ctx,
		"SELECT diff FROM audit_log WHERE org_id=$1 AND action='recorded_by_webhook' AND actor_user_id IS NULL",
		org.ID,
	).Scan(&diff); err != nil {
		t.Fatalf("querying recorded_by_webhook audit diff: %v", err)
	}
	var parsed struct {
		MatchedBy string `json:"matched_by"`
	}
	if err := json.Unmarshal(diff, &parsed); err != nil {
		t.Fatalf("decoding audit diff %s: %v", diff, err)
	}
	if parsed.MatchedBy != "metadata" {
		t.Fatalf("matched_by = %q, want metadata", parsed.MatchedBy)
	}
}
