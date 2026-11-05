package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	s := New("sk_test_dummy", secret)
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

	s := New("sk_test_dummy", secret)
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

	s := New("sk_test_dummy", "whsec_test_secret")
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

	s := New("sk_test_dummy", secret)
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

	s := New("sk_test_dummy", secret)
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
	if _, _, err := s.SendInvoice(ctx, inv, lines, "cus_test_1"); err != nil {
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
