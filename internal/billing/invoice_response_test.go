package billing_test

import (
	"slices"
	"testing"

	"shigoto/internal/billing"
	"shigoto/internal/core"
)

// TestInvoicePayOptionsOmitStripeWithoutURL covers the controller's finding
// 5 (from the /invoices response-shaping review): once an invoice is
// issued, stripe is only a real option if it has a provider URL to pay at.
func TestInvoicePayOptionsOmitStripeWithoutURL(t *testing.T) {
	opts := []string{"bank_transfer", "payid", "stripe"}
	cases := []struct {
		name string
		inv  core.Invoice
		want []string
	}{
		{"draft", core.Invoice{Status: "draft"}, opts},
		{"open, sent", core.Invoice{Status: "open", ProviderURL: "https://pay.example/1"}, opts},
		{"open, unsent", core.Invoice{Status: "open"}, []string{"bank_transfer", "payid"}},
	}
	for _, c := range cases {
		if got := billing.InvoicePayOptions(c.inv, opts); !slices.Equal(got, c.want) {
			t.Errorf("%s: pay options = %v, want %v", c.name, got, c.want)
		}
	}
}
