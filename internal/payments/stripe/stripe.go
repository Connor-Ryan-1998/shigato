// Package stripe implements payments.Provider against the real Stripe API.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	stripego "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"shigoto/internal/core"
	"shigoto/internal/payments"
)

// brisbane is the timezone Event.PaidOn is reported in, matching
// config.Config.Location's default.
var brisbane = mustLoadLocation("Australia/Brisbane")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

var _ payments.Provider = (*Stripe)(nil)

// Stripe is a payments.Provider backed by the real Stripe API.
type Stripe struct {
	client        *stripego.Client
	webhookSecret string
}

// New builds a Stripe provider for one org's credentials.
func New(secretKey, webhookSecret string) payments.Provider {
	return &Stripe{
		client:        stripego.NewClient(secretKey),
		webhookSecret: webhookSecret,
	}
}

// EnsureCustomer creates a Stripe customer for c, tagged with
// metadata.shigoto_contact_id.
func (s *Stripe) EnsureCustomer(ctx context.Context, c core.Contact) (string, error) {
	params := &stripego.CustomerCreateParams{
		Name:     stripego.String(c.Name),
		Metadata: map[string]string{"shigoto_contact_id": strconv.FormatInt(c.ID, 10)},
	}
	if c.Email != "" {
		params.Email = stripego.String(c.Email)
	}
	cust, err := s.client.V1Customers.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("stripe: creating customer: %w", err)
	}
	return cust.ID, nil
}

// lineDescription appends a GST suffix to a line's description. Shigoto
// owns the GST maths; the Stripe Tax product is not used, so the invoice
// item amount already includes GST and the description says so.
func lineDescription(l core.InvoiceLine) string {
	if l.GSTAmount == 0 {
		return l.Description
	}
	return fmt.Sprintf("%s (incl. GST)", l.Description)
}

// SendInvoice creates a draft Stripe invoice for inv, adds one invoice item
// per line (amount = line amount + GST, since Stripe Tax isn't used),
// finalizes it and sends it.
func (s *Stripe) SendInvoice(ctx context.Context, inv core.Invoice, lines []core.InvoiceLine, customerRef string) (string, string, error) {
	invParams := &stripego.InvoiceCreateParams{
		Params: stripego.Params{
			// Stable per shigoto invoice: a retried Create request (e.g. a
			// network blip between Stripe accepting the create and shigoto
			// receiving the response) replays onto the same Stripe invoice
			// instead of creating a duplicate draft.
			IdempotencyKey: stripego.String(fmt.Sprintf("shigoto-invoice-%d-%d", inv.OrgID, inv.ID)),
		},
		Customer:         stripego.String(customerRef),
		CollectionMethod: stripego.String(string(stripego.InvoiceCollectionMethodSendInvoice)),
		DaysUntilDue:     stripego.Int64(int64(inv.DueInDays)),
		Currency:         stripego.String("aud"),
		PaymentSettings: &stripego.InvoiceCreatePaymentSettingsParams{
			PaymentMethodTypes: stripego.StringSlice([]string{"card", "au_becs_debit"}),
		},
		Metadata: map[string]string{"shigoto_invoice_id": strconv.FormatInt(inv.ID, 10)},
		CustomFields: []*stripego.InvoiceCreateCustomFieldParams{
			{Name: stripego.String("Invoice No."), Value: stripego.String(invoiceNumber(inv))},
			{Name: stripego.String("Reference"), Value: stripego.String(inv.PaymentReference)},
		},
	}
	draft, err := s.client.V1Invoices.Create(ctx, invParams)
	if err != nil {
		return "", "", fmt.Errorf("stripe: creating invoice: %w", err)
	}

	for _, l := range lines {
		itemParams := &stripego.InvoiceItemCreateParams{
			Customer:    stripego.String(customerRef),
			Invoice:     stripego.String(draft.ID),
			Amount:      stripego.Int64(int64(l.Amount + l.GSTAmount)),
			Currency:    stripego.String("aud"),
			Description: stripego.String(lineDescription(l)),
		}
		if _, err := s.client.V1InvoiceItems.Create(ctx, itemParams); err != nil {
			return "", "", fmt.Errorf("stripe: creating invoice item: %w", err)
		}
	}

	finalized, err := s.client.V1Invoices.FinalizeInvoice(ctx, draft.ID, nil)
	if err != nil {
		return "", "", fmt.Errorf("stripe: finalizing invoice: %w", err)
	}
	sent, err := s.client.V1Invoices.SendInvoice(ctx, finalized.ID, nil)
	if err != nil {
		return "", "", fmt.Errorf("stripe: sending invoice: %w", err)
	}

	return sent.ID, sent.HostedInvoiceURL, nil
}

// invoiceNumber renders inv's shigoto number for the "Invoice No." custom
// field. IssueInvoice always assigns a number before SendInvoice is called,
// so a nil Number here would be a caller bug, not a normal case.
func invoiceNumber(inv core.Invoice) string {
	if inv.Number == nil {
		return ""
	}
	return strconv.Itoa(*inv.Number)
}

// VoidInvoice voids the Stripe invoice referenced by ref.
func (s *Stripe) VoidInvoice(ctx context.Context, ref string) error {
	if _, err := s.client.V1Invoices.VoidInvoice(ctx, ref, nil); err != nil {
		return fmt.Errorf("stripe: voiding invoice %s: %w", ref, err)
	}
	return nil
}

// paymentRef returns the stable provider_ref to record a webhook's payment
// against: the payment intent (or charge) id of the invoice's first
// recorded payment, when Stripe included one. A real invoice.paid webhook
// typically does NOT include the `payments` list unless explicitly
// expanded, and payment_intent is an expandable field that commonly arrives
// as a bare string id rather than an object — stripe-go's PaymentIntent and
// Charge types unmarshal either shape into just their ID, so this handles
// both without extra work. When no payment info is present at all, it
// falls back to the Stripe invoice id: invoice.paid fires once per invoice,
// so that id is still a stable, unique idempotency key for RecordPayment's
// provider_ref uniqueness.
func paymentRef(inv *stripego.Invoice) string {
	if inv.Payments != nil {
		for _, p := range inv.Payments.Data {
			if p.Payment == nil {
				continue
			}
			if p.Payment.PaymentIntent != nil && p.Payment.PaymentIntent.ID != "" {
				return p.Payment.PaymentIntent.ID
			}
			if p.Payment.Charge != nil && p.Payment.Charge.ID != "" {
				return p.Payment.Charge.ID
			}
		}
	}
	return inv.ID
}

// ParseWebhook verifies payload's signature with webhook.ConstructEvent and
// maps invoice.paid events to payments.Event; every other event type comes
// back as Kind="ignored".
func (s *Stripe) ParseWebhook(payload []byte, sigHeader string) (payments.Event, error) {
	event, err := webhook.ConstructEvent(payload, sigHeader, s.webhookSecret)
	if err != nil {
		return payments.Event{}, fmt.Errorf("%w: %v", payments.ErrBadSignature, err)
	}

	if event.Type != stripego.EventTypeInvoicePaid {
		return payments.Event{ID: event.ID, Kind: "ignored"}, nil
	}

	// Decoding into the library's own Invoice type (rather than a local
	// subset struct) matters here: its expandable fields (PaymentIntent,
	// Charge, ...) have custom UnmarshalJSON that accepts either a bare id
	// string or the full expanded object, matching what Stripe actually
	// sends.
	var inv stripego.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return payments.Event{}, fmt.Errorf("stripe: decoding invoice.paid payload: %w", err)
	}

	var paidAt int64
	if inv.StatusTransitions != nil {
		paidAt = inv.StatusTransitions.PaidAt
	}

	return payments.Event{
		ID:         event.ID,
		Kind:       "invoice.paid",
		InvoiceRef: inv.ID,
		PaymentRef: paymentRef(&inv),
		Amount:     core.Cents(inv.AmountPaid),
		PaidOn:     time.Unix(paidAt, 0).In(brisbane),
	}, nil
}
