//go:build integration

package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/payments"
	"shigoto/internal/testkit"
)

const webhookSecret = "whsec_test_secret"

// fakePayments resolves each org slug to its fake, and every other org to
// payments.ErrNoProvider.
func fakePayments(fakes map[string]*payments.Fake) testkit.HandlerOption {
	return testkit.Payments(func(orgSlug string) (payments.Provider, error) {
		f, ok := fakes[orgSlug]
		if !ok {
			return nil, payments.ErrNoProvider
		}
		return f, nil
	})
}

// webhookContactAndInvoice creates a contact and an issued invoice with
// provider_ref already set to providerRef, as if the issue route had
// already sent it to the provider.
func webhookContactAndInvoice(e *testkit.Env, org core.Org, providerRef string, totalCents int64) int64 {
	e.T.Helper()
	contact := e.Contact(org)
	inv := e.Invoice(org, contact.ID, testkit.Amount(totalCents), testkit.InvoiceGST("N-T"), testkit.Source("manual"), testkit.Issued(time.Now()))
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE invoices SET provider_ref = $1 WHERE org_id = $2 AND id = $3", providerRef, org.ID, inv.ID); err != nil {
		e.T.Fatalf("setting provider_ref: %v", err)
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

// stripeSig is the request headers a Stripe webhook delivery carries: a
// Stripe-Signature of sig and neither the JSON Content-Type nor the CSRF
// header that testkit sends by default (empty values delete them).
func stripeSig(sig string) map[string]string {
	return map[string]string{"Stripe-Signature": sig, "Content-Type": "", "X-Shigoto-CSRF": ""}
}

func TestWebhookPaysInvoice(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(e, org, "in_fake_1", 10000)
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	body := fakeWebhookBody(t, "evt_1", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(body), stripeSig(fake.Sign(body, time.Now())))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "paid" {
		t.Fatalf("status = %s, want paid", inv.Status)
	}

	if count := e.Count("payments", "org_id=$1 AND invoice_id=$2", org.ID, invID); count != 1 {
		t.Fatalf("payment rows = %d, want 1", count)
	}
}

func TestWebhookDuplicateDelivery(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(e, org, "in_fake_1", 10000)
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	body := fakeWebhookBody(t, "evt_dup", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	sig := fake.Sign(body, time.Now())
	deliver := func() testkit.Response {
		return hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(body), stripeSig(sig))
	}

	// Sequential redelivery.
	if rr := deliver(); rr.Code != http.StatusOK {
		t.Fatalf("first delivery: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}
	if rr := deliver(); rr.Code != http.StatusOK {
		t.Fatalf("second (sequential) delivery: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	// Concurrent redelivery of the same event.
	const n = 5
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = deliver().Code
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent delivery %d: status = %d, want 200", i, code)
		}
	}

	if eventCount := e.Count("webhook_events", "provider='stripe' AND event_id=$1", "evt_dup"); eventCount != 1 {
		t.Fatalf("webhook_events rows = %d, want 1", eventCount)
	}
	if paymentCount := e.Count("payments", "org_id=$1 AND invoice_id=$2", org.ID, invID); paymentCount != 1 {
		t.Fatalf("payment rows = %d, want 1", paymentCount)
	}
}

func TestWebhookBadSignature400NoChange(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(e, org, "in_fake_1", 10000)
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	body := fakeWebhookBody(t, "evt_bad", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	badSig := (&payments.Fake{Secret: "wrong_secret"}).Sign(body, time.Now())

	rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(body), stripeSig(badSig))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (unchanged)", inv.Status)
	}

	if count := e.Count("webhook_events", ""); count != 0 {
		t.Fatalf("webhook_events rows = %d, want 0", count)
	}
}

func TestWebhookUnknownOrg404(t *testing.T) {
	e := testkit.New(t)
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{})))

	body := fakeWebhookBody(t, "evt_1", "invoice.paid", "in_fake_1", "pi_fake_1", 10000, time.Now())
	rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/no-such-org", bytes.NewReader(body), stripeSig("t=1,v1=deadbeef"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body)
	}
}

func TestIssueSendFailureKeepsNumberAndRetries(t *testing.T) {
	e := testkit.New(t)

	// The first send fails part-way, after one of the two invoice items
	// has been created provider-side.
	fake := &payments.Fake{Secret: webhookSecret, SendInvoiceErr: fmt.Errorf("stripe is down"), SendInvoiceErrAfterItems: 1}
	handler := e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake}))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	draft, err := core.CreateDraftInvoice(e.Ctx, e.Pool, org.ID, core.NewInvoice{
		ContactID: contact.ID, Source: "manual", DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "a", QuantityMilli: 1000, UnitAmount: 1000, GSTCode: "N-T"},
			{Description: "b", QuantityMilli: 1000, UnitAmount: 2000, GSTCode: "N-T"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	draftID := draft.ID

	rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" || inv.Number == nil || *inv.Number != 1 {
		t.Fatalf("after failed send: status=%s number=%v, want open, 1", inv.Status, inv.Number)
	}
	if inv.ProviderRef != "" {
		t.Fatalf("after failed send: provider_ref = %q, want empty", inv.ProviderRef)
	}

	rr = owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("send retry: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	inv, err = core.GetInvoice(e.Ctx, e.Pool, org.ID, draftID)
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
	e := testkit.New(t)
	fake := &payments.Fake{Secret: webhookSecret}
	handler := e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake}))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	draftID := e.Invoice(org, contact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual")).ID

	rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	// The fake has no SendInvoiceErr, so issuing already sent successfully.
	// A /send retry on an already-sent invoice must be rejected rather
	// than sending a second time.
	rr = owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", draftID), nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("resend: status = %d, want 409, body=%s", rr.Code, rr.Body)
	}

	if calls := fake.Calls(); len(calls) != 1 {
		t.Fatalf("fake SendInvoice calls = %d, want 1 (no double-send)", len(calls))
	}
}

// TestSendPastDueRejected covers finding 6: billing refuses to send an
// invoice whose due date has passed before touching the provider at all
// (no EnsureCustomer), and the route maps that to 409.
func TestSendPastDueRejected(t *testing.T) {
	e := testkit.New(t)
	fake := &payments.Fake{Secret: webhookSecret}
	handler := e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake}))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	// Issued 30 days ago on a 14-day term: due 16 days ago, never sent.
	draftID := e.Invoice(org, contact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual"),
		testkit.DueIn(14), testkit.Issued(time.Now().AddDate(0, 0, -30))).ID

	rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", draftID), nil)
	if rr.Code != http.StatusConflict || !strings.Contains(string(rr.Body), "due date has passed; void and reissue") {
		t.Fatalf("send: status = %d body=%s, want 409 with 'due date has passed; void and reissue'", rr.Code, rr.Body)
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
	e := testkit.New(t)
	fake := &payments.Fake{Secret: webhookSecret}
	handler := e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake}))
	_, owner := e.OwnerSession("acme", handler)
	other := e.Org("other")
	otherContact := e.Contact(other)
	otherDraftID := e.Invoice(other, otherContact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual")).ID

	post := func(id int64) testkit.Response {
		return owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/send", id), nil)
	}

	if rr := post(999999); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown invoice id: status = %d, want 404, body=%s", rr.Code, rr.Body)
	}
	if rr := post(otherDraftID); rr.Code != http.StatusNotFound {
		t.Fatalf("cross-org invoice id: status = %d, want 404, body=%s", rr.Code, rr.Body)
	}
}

func TestVoidCallsProviderVoidInvoice(t *testing.T) {
	e := testkit.New(t)
	fake := &payments.Fake{Secret: webhookSecret}
	handler := e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake}))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	draftID := e.Invoice(org, contact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual")).ID

	rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.ProviderRef == "" {
		t.Fatalf("precondition: provider_ref is empty, want set after a successful send")
	}

	rr = owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/void", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("void: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	voided := fake.VoidedRefs()
	if len(voided) != 1 || voided[0] != inv.ProviderRef {
		t.Fatalf("fake voided = %v, want [%s]", voided, inv.ProviderRef)
	}

	inv, err = core.GetInvoice(e.Ctx, e.Pool, org.ID, draftID)
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
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules(), fakePayments(nil))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	draftID := e.Invoice(org, contact.ID, testkit.Amount(10000), testkit.InvoiceGST("N-T"), testkit.Source("manual")).ID
	rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	// A partial payment, not the full amount: it still counts as "has
	// payments" for the void guard.
	rr = owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/payments", draftID),
		map[string]any{"amount": 4000, "paid_on": "2026-11-05", "method": "cash"})
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("record payment: status = %d, want 200/201, body=%s", rr.Code, rr.Body)
	}

	rr = owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/void", draftID), nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("void: status = %d, want 409, body=%s", rr.Code, rr.Body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body, &errBody); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}
	if errBody.Error != "invoice has payments; record a credit note instead" {
		t.Fatalf("error = %q, want %q", errBody.Error, "invoice has payments; record a credit note instead")
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (refused void must not change it)", inv.Status)
	}
}

func TestVoidProviderFailureLeavesOpen(t *testing.T) {
	e := testkit.New(t)
	fake := &payments.Fake{Secret: webhookSecret}
	handler := e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake}))
	org, owner := e.OwnerSession("acme", handler)
	contact := e.Contact(org)

	draftID := e.Invoice(org, contact.ID, testkit.Amount(1000), testkit.InvoiceGST("N-T"), testkit.Source("manual")).ID

	rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draftID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	fake.VoidInvoiceErr = fmt.Errorf("stripe is down")

	rr = owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/void", draftID), nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("void: status = %d, want 502, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, draftID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "open" {
		t.Fatalf("status = %s, want open (the DB void must roll back with the failed provider void)", inv.Status)
	}
}

// webhookAuditCount counts audit_log rows for org with the given action and
// no actor (the webhook's own rows).
func webhookAuditCount(e *testkit.Env, org core.Org, action string) int {
	e.T.Helper()
	return e.Count("audit_log", "org_id=$1 AND action=$2 AND actor_user_id IS NULL", org.ID, action)
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
	e := testkit.New(t)
	org := e.Org("acme")
	fake := &payments.Fake{Secret: webhookSecret}
	invID := webhookContactAndInvoice(e, org, "in_fake_1", 10000)
	voidID := webhookContactAndInvoice(e, org, "in_fake_void", 10000)
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE invoices SET status='void' WHERE id=$1", voidID); err != nil {
		t.Fatalf("voiding: %v", err)
	}
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	zero := fakeWebhookBody(t, "evt_zero", "invoice.paid", "in_fake_1", "pi_zero", 0, time.Now())
	if rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(zero), stripeSig(fake.Sign(zero, time.Now()))); rr.Code != http.StatusOK {
		t.Fatalf("zero amount: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}
	void := fakeWebhookBody(t, "evt_void", "invoice.paid", "in_fake_void", "pi_void", 10000, time.Now())
	if rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(void), stripeSig(fake.Sign(void, time.Now()))); rr.Code != http.StatusOK {
		t.Fatalf("void invoice: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	if n := e.Count("payments", "org_id=$1", org.ID); n != 0 {
		t.Fatalf("payments = %d, want 0", n)
	}
	if n := webhookAuditCount(e, org, "payment_rejected"); n != 2 {
		t.Fatalf("payment_rejected audit rows = %d, want 2", n)
	}
	if events := e.Count("webhook_events", "org_id=$1", org.ID); events != 2 {
		t.Fatalf("webhook_events = %d, want 2 (both committed so redelivery is a no-op)", events)
	}
	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, invID)
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
	e := testkit.New(t)
	org := e.Org("acme")
	other := e.Org("other")
	otherInv := webhookContactAndInvoice(e, other, "in_other", 10000)
	fake := &payments.Fake{Secret: webhookSecret}
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	for _, body := range [][]byte{
		fakeWebhookBody(t, "evt_orphan", "invoice.paid", "in_unknown", "pi_orphan", 10000, time.Now()),
		metadataWebhookBody(t, "evt_cross", "in_unknown_2", otherInv, 10000),
	} {
		if rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(body), stripeSig(fake.Sign(body, time.Now()))); rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
		}
	}

	if n := webhookAuditCount(e, org, "orphan_event"); n != 2 {
		t.Fatalf("orphan_event audit rows = %d, want 2", n)
	}
	if n := e.Count("payments", "org_id=$1", other.ID); n != 0 {
		t.Fatalf("other org payments = %d, want 0", n)
	}
	inv, err := core.GetInvoice(e.Ctx, e.Pool, other.ID, otherInv)
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
	e := testkit.New(t)
	org := e.Org("acme")
	invID := webhookContactAndInvoice(e, org, "in_fake_1", 10000)
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE invoices SET provider_ref=NULL WHERE id=$1", invID); err != nil {
		t.Fatalf("clearing provider_ref: %v", err)
	}
	fake := &payments.Fake{Secret: webhookSecret}
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	body := metadataWebhookBody(t, "evt_meta", "in_never_stored", invID, 10000)
	if rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(body), stripeSig(fake.Sign(body, time.Now()))); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.Status != "paid" {
		t.Fatalf("status = %s, want paid", inv.Status)
	}
	if n := webhookAuditCount(e, org, "orphan_event"); n != 0 {
		t.Fatalf("orphan_event audit rows = %d, want 0", n)
	}
}

// TestWebhookMetadataFallbackBackfillsRef covers the same metadata-fallback
// match as TestWebhookMetadataFallback, but additionally checks that the
// never-committed provider_ref is backfilled from the event, and that the
// recorded_by_webhook audit diff records how the invoice was matched.
func TestWebhookMetadataFallbackBackfillsRef(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	invID := webhookContactAndInvoice(e, org, "in_fake_1", 10000)
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE invoices SET provider_ref=NULL WHERE id=$1", invID); err != nil {
		t.Fatalf("clearing provider_ref: %v", err)
	}
	fake := &payments.Fake{Secret: webhookSecret}
	hook := e.Anonymous(e.Handler(testkit.OnlyModules(), fakePayments(map[string]*payments.Fake{"acme": fake})))

	const wantRef = "in_never_stored"
	body := metadataWebhookBody(t, "evt_meta_backfill", wantRef, invID, 10000)
	if rr := hook.DoWith(http.MethodPost, "/webhooks/stripe/acme", bytes.NewReader(body), stripeSig(fake.Sign(body, time.Now()))); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body)
	}

	inv, err := core.GetInvoice(e.Ctx, e.Pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if inv.ProviderRef != wantRef {
		t.Fatalf("provider_ref = %q, want %q (backfilled from the event)", inv.ProviderRef, wantRef)
	}

	var diff []byte
	if err := e.Pool.QueryRow(e.Ctx,
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
