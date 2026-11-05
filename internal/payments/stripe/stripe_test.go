package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"shigoto/internal/core"
	"shigoto/internal/payments"
)

// brisbane is the timezone most tests pass to New, matching
// config.Config.Location's default.
var brisbane = func() *time.Location {
	loc, err := time.LoadLocation("Australia/Brisbane")
	if err != nil {
		panic(err)
	}
	return loc
}()

// loadFixture reads testdata/name, substituting the __API_VERSION__
// placeholder for the stripe-go library's current API version, so the
// fixture always passes webhook.ConstructEvent's version check regardless
// of which stripe-go release is vendored.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return []byte(strings.ReplaceAll(string(raw), "__API_VERSION__", stripego.APIVersion))
}

func TestParseWebhookInvoicePaid(t *testing.T) {
	payload := loadFixture(t, "invoice_paid.json")
	const secret = "whsec_test_secret"

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  secret,
	})

	s := New("sk_test_dummy", secret, brisbane)
	event, err := s.ParseWebhook(signed.Payload, signed.Header)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}

	if event.ID != "evt_test_invoice_paid" {
		t.Errorf("ID = %q, want evt_test_invoice_paid", event.ID)
	}
	if event.Kind != "invoice.paid" {
		t.Errorf("Kind = %q, want invoice.paid", event.Kind)
	}
	if event.InvoiceRef != "in_test_123" {
		t.Errorf("InvoiceRef = %q, want in_test_123", event.InvoiceRef)
	}
	if event.PaymentRef != "pi_test_456" {
		t.Errorf("PaymentRef = %q, want pi_test_456", event.PaymentRef)
	}
	if event.Amount != 11000 {
		t.Errorf("Amount = %d, want 11000", event.Amount)
	}
	if event.ShigotoInvoiceID != 42 {
		t.Errorf("ShigotoInvoiceID = %d, want 42", event.ShigotoInvoiceID)
	}
	wantPaidOn := time.Unix(1700000100, 0).In(brisbane)
	if !event.PaidOn.Equal(wantPaidOn) {
		t.Errorf("PaidOn = %v, want %v", event.PaidOn, wantPaidOn)
	}
}

// TestParseWebhookInvoicePaidNoPaymentsExpanded covers a real invoice.paid
// webhook, which typically does not include the `payments` list unless
// explicitly expanded: PaymentRef must fall back to the stable Stripe
// invoice id rather than come back empty (which would break RecordPayment's
// provider_ref idempotency) or fail to decode.
func TestParseWebhookInvoicePaidNoPaymentsExpanded(t *testing.T) {
	payload := loadFixture(t, "invoice_paid_no_payments.json")
	const secret = "whsec_test_secret"

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  secret,
	})

	s := New("sk_test_dummy", secret, brisbane)
	event, err := s.ParseWebhook(signed.Payload, signed.Header)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}

	if event.InvoiceRef != "in_test_789" {
		t.Errorf("InvoiceRef = %q, want in_test_789", event.InvoiceRef)
	}
	if event.PaymentRef != "in_test_789" {
		t.Errorf("PaymentRef = %q, want in_test_789 (falls back to invoice id)", event.PaymentRef)
	}
	if event.Amount != 5000 {
		t.Errorf("Amount = %d, want 5000", event.Amount)
	}
}

func TestParseWebhookBadSignature(t *testing.T) {
	payload := loadFixture(t, "invoice_paid.json")

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  "whsec_wrong_secret",
	})

	s := New("sk_test_dummy", "whsec_test_secret", brisbane)
	_, err := s.ParseWebhook(signed.Payload, signed.Header)
	if !errors.Is(err, payments.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

// TestParseWebhookAPIVersionMismatch covers an endpoint pinned to a Stripe
// API version other than the one stripe-go was built for: the event must
// still parse (shigoto only reads stable invoice fields), not fail as a
// "bad signature".
func TestParseWebhookAPIVersionMismatch(t *testing.T) {
	raw, err := os.ReadFile("testdata/invoice_paid.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	payload := []byte(strings.ReplaceAll(string(raw), "__API_VERSION__", "2020-08-27"))
	const secret = "whsec_test_secret"

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret})

	s := New("sk_test_dummy", secret, brisbane)
	event, err := s.ParseWebhook(signed.Payload, signed.Header)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if event.Kind != "invoice.paid" || event.InvoiceRef != "in_test_123" {
		t.Errorf("event = %+v, want invoice.paid for in_test_123", event)
	}
}

// TestParseWebhookMalformedPayloadNotBadSignature checks that only
// signature failures map to payments.ErrBadSignature: a correctly signed
// but undecodable payload is a different error.
func TestParseWebhookMalformedPayloadNotBadSignature(t *testing.T) {
	const secret = "whsec_test_secret"
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: []byte("not json"), Secret: secret})

	s := New("sk_test_dummy", secret, brisbane)
	_, err := s.ParseWebhook(signed.Payload, signed.Header)
	if err == nil || errors.Is(err, payments.ErrBadSignature) {
		t.Fatalf("err = %v, want a non-signature error", err)
	}
}

// TestSendInvoiceIdempotencyKeys checks every Stripe write SendInvoice and
// EnsureCustomer make carries a deterministic idempotency key, so a retry
// after a partial failure replays onto the same Stripe objects instead of
// duplicating invoice items (overcharging) or customers. It runs against a
// local httptest server, not the network.
func TestSendInvoiceIdempotencyKeys(t *testing.T) {
	var mu sync.Mutex
	keys := map[string]string{} // "METHOD path[#n]" -> Idempotency-Key
	items := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		k := r.Method + " " + r.URL.Path
		id := "in_test_1"
		switch r.URL.Path {
		case "/v1/invoiceitems":
			items++
			k = fmt.Sprintf("%s#%d", k, items)
			id = fmt.Sprintf("ii_test_%d", items)
		case "/v1/customers":
			id = "cus_test_1"
		}
		keys[k] = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"hosted_invoice_url":"https://stripe.example/i"}`, id)
	}))
	defer srv.Close()

	backends := stripego.NewBackendsWithConfig(&stripego.BackendConfig{
		URL:               stripego.String(srv.URL),
		MaxNetworkRetries: stripego.Int64(0),
		HTTPClient:        srv.Client(),
	})
	s := &Stripe{client: stripego.NewClient("sk_test_dummy", stripego.WithBackends(backends))}

	ctx := context.Background()
	if _, err := s.EnsureCustomer(ctx, core.Contact{ID: 7, OrgID: 3, Name: "C"}); err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	inv := core.Invoice{ID: 9, OrgID: 3, DueInDays: 14}
	lines := []core.InvoiceLine{{ID: 11, Amount: 100}, {ID: 12, Amount: 200}}
	if _, _, err := s.SendInvoice(ctx, inv, lines, "cus_test_1", time.Now()); err != nil {
		t.Fatalf("SendInvoice: %v", err)
	}

	want := map[string]string{
		"POST /v1/customers":                   "shigoto-customer-3-7",
		"POST /v1/invoices":                    "shigoto-invoice-3-9",
		"POST /v1/invoiceitems#1":              "shigoto-invoice-3-9-item-11",
		"POST /v1/invoiceitems#2":              "shigoto-invoice-3-9-item-12",
		"POST /v1/invoices/in_test_1/finalize": "shigoto-invoice-3-9-finalize",
		"POST /v1/invoices/in_test_1/send":     "shigoto-invoice-3-9-send",
	}
	for k, v := range want {
		if keys[k] != v {
			t.Errorf("%s Idempotency-Key = %q, want %q", k, keys[k], v)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("requests = %v, want exactly %d", keys, len(want))
	}
}

// newCreateInvoiceStub starts an httptest server that stands in for Stripe,
// recording the form params of the POST /v1/invoices request (Create), and
// answering every request with the minimal JSON SendInvoice needs to
// proceed.
func newCreateInvoiceStub(t *testing.T) (s *Stripe, params func() url.Values) {
	t.Helper()
	var mu sync.Mutex
	var createParams url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/invoices" && r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parsing form: %v", err)
			}
			mu.Lock()
			createParams = r.PostForm
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"in_test_1","hosted_invoice_url":"https://stripe.example/i"}`)
	}))
	t.Cleanup(srv.Close)

	backends := stripego.NewBackendsWithConfig(&stripego.BackendConfig{
		URL:               stripego.String(srv.URL),
		MaxNetworkRetries: stripego.Int64(0),
		HTTPClient:        srv.Client(),
	})
	return &Stripe{client: stripego.NewClient("sk_test_dummy", stripego.WithBackends(backends))}, func() url.Values {
		mu.Lock()
		defer mu.Unlock()
		return createParams
	}
}

// TestSendInvoiceAutoAdvanceFalse checks the Create Invoice call always
// disables Stripe's auto_advance: shigoto drives finalize/send/void
// explicitly and must not race Stripe's own invoice-advancing clock.
func TestSendInvoiceAutoAdvanceFalse(t *testing.T) {
	s, params := newCreateInvoiceStub(t)

	inv := core.Invoice{ID: 9, OrgID: 3, DueInDays: 14}
	if _, _, err := s.SendInvoice(context.Background(), inv, nil, "cus_test_1", time.Now()); err != nil {
		t.Fatalf("SendInvoice: %v", err)
	}

	if got := params().Get("auto_advance"); got != "false" {
		t.Errorf("auto_advance = %q, want %q", got, "false")
	}
}

// TestDaysUntilDueFromToday checks days_until_due is computed fresh from
// inv.DueDate and today, floored at 0, rather than replaying the
// due_in_days fixed at issue time - a retried send can happen well after
// issue.
func TestDaysUntilDueFromToday(t *testing.T) {
	due := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	inv := core.Invoice{ID: 9, OrgID: 3, DueDate: &due}

	cases := []struct {
		today time.Time
		want  string
	}{
		{time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), "2"},
		{time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC), "0"},
	}
	for _, c := range cases {
		s, params := newCreateInvoiceStub(t)
		if _, _, err := s.SendInvoice(context.Background(), inv, nil, "cus_test_1", c.today); err != nil {
			t.Fatalf("SendInvoice(today=%v): %v", c.today, err)
		}
		if got := params().Get("days_until_due"); got != c.want {
			t.Errorf("today=%v: days_until_due = %q, want %q", c.today, got, c.want)
		}
	}
}

// TestParseWebhookPaidOnUsesLocation checks Event.PaidOn is reported in the
// provider's configured loc, not a hardcoded timezone.
func TestParseWebhookPaidOnUsesLocation(t *testing.T) {
	payload := loadFixture(t, "invoice_paid.json")
	payload = []byte(strings.Replace(string(payload), `"paid_at": 1700000100`, `"paid_at": 1762351200`, 1))
	const secret = "whsec_test_secret"

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret})

	s := New("sk_test_dummy", secret, brisbane)
	event, err := s.ParseWebhook(signed.Payload, signed.Header)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	y, m, d := event.PaidOn.Date()
	if y != 2025 || m != time.November || d != 6 {
		t.Errorf("PaidOn date = %04d-%02d-%02d, want 2025-11-06", y, m, d)
	}
}
