// Package stripe implements payments.Provider against the real Stripe API.
package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	stripego "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"shigoto/internal/core"
	"shigoto/internal/payments"
)

var _ payments.Provider = (*Stripe)(nil)

// Stripe is a payments.Provider backed by the real Stripe API.
type Stripe struct {
	client        *stripego.Client
	webhookSecret string
	// loc is the timezone Event.PaidOn is reported in.
	loc *time.Location
}

// New builds a Stripe provider for one org's credentials. loc is the
// timezone Event.PaidOn is reported in (config.Config.Location).
func New(secretKey, webhookSecret string, loc *time.Location) payments.Provider {
	return &Stripe{
		client:        stripego.NewClient(secretKey),
		webhookSecret: webhookSecret,
		loc:           loc,
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
	params.SetIdempotencyKey(payments.CustomerKey(c))
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

// dueDateUnix returns the Stripe `due_date` to send for inv: the Unix
// timestamp of inv.DueDate at 23:59:59 in loc. It is absolute (not a day
// count from "today"), so it is identical on every retry regardless of what
// calendar day the retry happens on - unlike a days-until-due figure, it can
// safely share SendInvoice's fixed idempotency key: Stripe rejects a
// retried idempotency key whose params differ from the first attempt
// (idempotency_error), which a day-count recomputed on a later day would
// trigger.
func dueDateUnix(dueDate time.Time, loc *time.Location) int64 {
	y, m, d := dueDate.Date()
	return time.Date(y, m, d, 23, 59, 59, 0, loc).Unix()
}

// locOrUTC returns s.loc, defaulting to UTC.
func (s *Stripe) locOrUTC() *time.Location {
	if s.loc == nil {
		return time.UTC
	}
	return s.loc
}

// SendInvoice creates a draft Stripe invoice for inv, adds one invoice item
// per line (amount = line amount + GST, since Stripe Tax isn't used),
// finalizes it and sends it. today is the caller's current time, checked
// against inv's due date: if the due date has already passed, SendInvoice
// makes no Stripe call and returns an error wrapping core.ErrInvalidState,
// since sending a past-due invoice to the provider now would misrepresent
// it; the caller should void and reissue instead.
func (s *Stripe) SendInvoice(ctx context.Context, inv core.Invoice, lines []core.InvoiceLine, customerRef string, today time.Time) (string, string, error) {
	if inv.DueDate == nil {
		return "", "", fmt.Errorf("stripe: invoice %d has no due date: %w", inv.ID, core.ErrInvalidState)
	}
	loc := s.locOrUTC()
	dueUnix := dueDateUnix(*inv.DueDate, loc)
	if dueUnix <= today.Unix() {
		slog.Warn("due date has passed; void and reissue", "invoice_id", inv.ID, "due_date", time.Unix(dueUnix, 0).In(loc))
		return "", "", fmt.Errorf("stripe: invoice %d due date has passed: %w", inv.ID, core.ErrInvalidState)
	}

	// Every write carries a key derived from shigoto ids, so a retried
	// send (a network blip, or POST .../send after a partial failure)
	// replays onto the same Stripe invoice and items instead of creating
	// duplicates. ponytail: Stripe keeps idempotency keys for 24h; a retry
	// later than that after a partial failure could still duplicate items.
	key := payments.InvoiceKey(inv)
	invParams := &stripego.InvoiceCreateParams{
		Params:           stripego.Params{IdempotencyKey: stripego.String(key)},
		Customer:         stripego.String(customerRef),
		CollectionMethod: stripego.String(string(stripego.InvoiceCollectionMethodSendInvoice)),
		// shigoto, not Stripe, drives sending: AutoAdvance would let
		// Stripe auto-finalize/auto-collect/auto-mark-uncollectible a
		// draft invoice on its own clock, racing shigoto's explicit
		// finalize+send below and any later void.
		AutoAdvance: stripego.Bool(false),
		DueDate:     stripego.Int64(dueUnix),
		Currency:    stripego.String("aud"),
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
			Params:      stripego.Params{IdempotencyKey: stripego.String(fmt.Sprintf("%s-item-%d", key, l.ID))},
			Amount:      stripego.Int64(int64(l.Amount + l.GSTAmount)),
			Currency:    stripego.String("aud"),
			Description: stripego.String(lineDescription(l)),
		}
		if _, err := s.client.V1InvoiceItems.Create(ctx, itemParams); err != nil {
			return "", "", fmt.Errorf("stripe: creating invoice item: %w", err)
		}
	}

	finalized, err := s.client.V1Invoices.FinalizeInvoice(ctx, draft.ID, &stripego.InvoiceFinalizeInvoiceParams{
		Params: stripego.Params{IdempotencyKey: stripego.String(key + "-finalize")},
	})
	if err != nil {
		return "", "", fmt.Errorf("stripe: finalizing invoice: %w", err)
	}
	sent, err := s.client.V1Invoices.SendInvoice(ctx, finalized.ID, &stripego.InvoiceSendInvoiceParams{
		Params: stripego.Params{IdempotencyKey: stripego.String(key + "-send")},
	})
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

// ParseWebhook verifies payload's signature and maps invoice.paid events to payments.Event; every other event type comes
// back as Kind="ignored".
func (s *Stripe) ParseWebhook(payload []byte, sigHeader string) (payments.Event, error) {
	// The endpoint's API version is whatever the operator configured in
	// the Stripe dashboard, not necessarily the one stripe-go was built
	// for. shigoto only reads stable invoice fields, so a mismatch must not
	// reject every event.
	event, err := webhook.ConstructEventWithOptions(payload, sigHeader, s.webhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		switch {
		case errors.Is(err, webhook.ErrInvalidHeader), errors.Is(err, webhook.ErrNoValidSignature),
			errors.Is(err, webhook.ErrNotSigned), errors.Is(err, webhook.ErrTooOld):
			return payments.Event{}, fmt.Errorf("%w: %v", payments.ErrBadSignature, err)
		}
		return payments.Event{}, fmt.Errorf("stripe: constructing webhook event: %w", err)
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

	// A malformed or missing id just disables the metadata fallback.
	shigotoID, _ := strconv.ParseInt(inv.Metadata["shigoto_invoice_id"], 10, 64)

	var paidAt int64
	if inv.StatusTransitions != nil {
		paidAt = inv.StatusTransitions.PaidAt
	}
	// A real invoice.paid webhook always carries paid_at, but a replayed
	// or synthetic event might not: fall back to the event's own created
	// time rather than reporting a payment at the Unix epoch.
	if paidAt == 0 {
		paidAt = event.Created
	}

	return payments.Event{
		ID:               event.ID,
		Kind:             "invoice.paid",
		InvoiceRef:       inv.ID,
		PaymentRef:       paymentRef(&inv),
		Amount:           core.Cents(inv.AmountPaid),
		PaidOn:           time.Unix(paidAt, 0).In(s.locOrUTC()),
		ShigotoInvoiceID: shigotoID,
	}, nil
}
