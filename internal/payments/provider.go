// Package payments defines the interface shigoto uses to send invoices to,
// and receive paid webhooks from, a payment provider (Stripe in production;
// Fake in tests).
package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"shigoto/internal/core"
)

// ErrBadSignature is returned by Provider.ParseWebhook when the payload's
// signature doesn't verify against the provider's webhook secret.
var ErrBadSignature = errors.New("bad webhook signature")

// ErrNoProvider is returned by httpapi.Deps.Payments when an org has no
// provider credentials configured. Issuing still works without a provider;
// there's just no provider_url, and the invoice relies on bank/PayID.
var ErrNoProvider = errors.New("no payment provider configured")

// Provider sends invoices to, and receives payment notifications from, an
// external payment processor. Stripe is not the source of truth: invoice
// numbers, amounts and status live in Postgres, and provider IDs are only
// references.
type Provider interface {
	// EnsureCustomer creates (or returns the existing) provider-side
	// customer for c, to be stored in contacts.provider_customer_ref.
	EnsureCustomer(ctx context.Context, c core.Contact) (ref string, err error)
	// SendInvoice creates, finalizes and sends a hosted invoice for inv.
	// today is the caller's current date (in its own timezone), used to
	// compute the days-until-due the provider is told, since inv.DueDate
	// was fixed at issue time and a retried send may happen later. It
	// returns the provider's reference (to store in
	// invoices.provider_ref) and the hosted payment page URL.
	SendInvoice(ctx context.Context, inv core.Invoice, lines []core.InvoiceLine, customerRef string, today time.Time) (ref, hostedURL string, err error)
	// VoidInvoice voids the provider-side invoice referenced by ref.
	VoidInvoice(ctx context.Context, ref string) error
	// ParseWebhook verifies payload's signature against sigHeader and
	// decodes it into an Event. It returns ErrBadSignature if the
	// signature doesn't match.
	ParseWebhook(payload []byte, sigHeader string) (Event, error)
}

// Event is a provider webhook event, normalized to what shigoto needs to
// record a payment.
type Event struct {
	// ID is the provider's event id, used to dedupe webhook_events.
	ID string
	// Kind is "invoice.paid" or "ignored" for every other event type.
	Kind string
	// InvoiceRef is the provider's invoice reference (invoices.provider_ref).
	InvoiceRef string
	// PaymentRef is the provider's charge/payment-intent id, stored as
	// payments.provider_ref.
	PaymentRef string
	Amount     core.Cents
	PaidOn     time.Time
	// ShigotoInvoiceID is the shigoto invoice id the provider echoed back
	// from metadata (0 if absent). The webhook falls back to it when
	// InvoiceRef matches no invoice, e.g. when Stripe accepted a send but
	// shigoto never committed the returned provider_ref.
	ShigotoInvoiceID int64
}

// InvoiceKey is the idempotency key for creating inv on the provider.
// Every other write for inv derives from it ("-item-<lineID>",
// "-finalize", "-send"), so a retried send replays onto the same
// provider objects instead of duplicating them.
func InvoiceKey(inv core.Invoice) string {
	return fmt.Sprintf("shigoto-invoice-%d-%d", inv.OrgID, inv.ID)
}

// CustomerKey is the idempotency key for creating c's provider customer.
func CustomerKey(c core.Contact) string {
	return fmt.Sprintf("shigoto-customer-%d-%d", c.OrgID, c.ID)
}
