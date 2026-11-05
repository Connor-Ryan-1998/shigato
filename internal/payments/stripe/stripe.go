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

// webhookInvoice is the subset of Stripe's invoice object ParseWebhook
// needs from an invoice.paid event's data.object.
type webhookInvoice struct {
	ID                string `json:"id"`
	AmountPaid        int64  `json:"amount_paid"`
	StatusTransitions struct {
		PaidAt int64 `json:"paid_at"`
	} `json:"status_transitions"`
	Payments struct {
		Data []struct {
			Payment struct {
				Type   string `json:"type"`
				Charge *struct {
					ID string `json:"id"`
				} `json:"charge"`
				PaymentIntent *struct {
					ID string `json:"id"`
				} `json:"payment_intent"`
			} `json:"payment"`
		} `json:"data"`
	} `json:"payments"`
}

// paymentRef returns the charge or payment intent id of the invoice's first
// recorded payment, preferring a payment intent id (shigoto's payments use
// collection_method=send_invoice, which attaches a PaymentIntent).
func (inv webhookInvoice) paymentRef() string {
	for _, p := range inv.Payments.Data {
		if p.Payment.PaymentIntent != nil {
			return p.Payment.PaymentIntent.ID
		}
		if p.Payment.Charge != nil {
			return p.Payment.Charge.ID
		}
	}
	return ""
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

	var inv webhookInvoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return payments.Event{}, fmt.Errorf("stripe: decoding invoice.paid payload: %w", err)
	}

	return payments.Event{
		ID:         event.ID,
		Kind:       "invoice.paid",
		InvoiceRef: inv.ID,
		PaymentRef: inv.paymentRef(),
		Amount:     core.Cents(inv.AmountPaid),
		PaidOn:     time.Unix(inv.StatusTransitions.PaidAt, 0).In(brisbane),
	}, nil
}
