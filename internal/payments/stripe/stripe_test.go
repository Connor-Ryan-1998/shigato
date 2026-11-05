package stripe

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

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
	wantPaidOn := time.Unix(1700000100, 0).In(brisbane)
	if !event.PaidOn.Equal(wantPaidOn) {
		t.Errorf("PaidOn = %v, want %v", event.PaidOn, wantPaidOn)
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
