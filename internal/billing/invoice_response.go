package billing

import (
	"context"
	"slices"

	"shigoto/internal/core"
)

// dateLayout matches the other packages' own date encoding (YYYY-MM-DD).
const dateLayout = "2006-01-02"

// LineResponse is one invoice line as returned by the API.
type LineResponse struct {
	ID            int64  `json:"id"`
	Description   string `json:"description"`
	QuantityMilli int64  `json:"quantity_milli"`
	UnitAmount    int64  `json:"unit_amount"`
	GSTCode       string `json:"gst_code"`
	GSTAmount     int64  `json:"gst_amount"`
	Amount        int64  `json:"amount"`
}

// InvoiceResponse is an invoice as returned by the API: core.Invoice
// reshaped to snake_case JSON, with dates as YYYY-MM-DD and with
// org-internal/provider-internal fields (OrgID, ProviderRef, ProviderURL)
// left out.
type InvoiceResponse struct {
	ID               int64          `json:"id"`
	ContactID        int64          `json:"contact_id"`
	ContactName      string         `json:"contact_name"`
	Number           *int           `json:"number"`
	Source           string         `json:"source"`
	Status           string         `json:"status"`
	IssueDate        *string        `json:"issue_date"`
	DueDate          *string        `json:"due_date"`
	Currency         string         `json:"currency"`
	Subtotal         int64          `json:"subtotal"`
	GSTTotal         int64          `json:"gst_total"`
	Total            int64          `json:"total"`
	AmountPaid       int64          `json:"amount_paid"`
	Outstanding      int64          `json:"outstanding"`
	PaymentReference string         `json:"payment_reference"`
	PayOptions       []string       `json:"pay_options"`
	Lines            []LineResponse `json:"lines"`
}

// InvoicePayOptions narrows the org's pay options to what inv can actually
// be paid with: an issued invoice that never reached the provider (no
// ProviderURL) has nowhere to pay by card, so stripe is dropped.
func InvoicePayOptions(inv core.Invoice, opts []string) []string {
	if inv.Status == "draft" || inv.ProviderURL != "" {
		return opts
	}
	return slices.DeleteFunc(slices.Clone(opts), func(o string) bool { return o == "stripe" })
}

// InvoiceJSON builds inv's API representation, including the org's current
// pay options (via s.PayOptions) narrowed by InvoicePayOptions. q is the
// querier to read the org's pay-option settings from (the caller's
// transaction, or s.Pool outside one).
func (s *Service) InvoiceJSON(ctx context.Context, q core.Querier, inv core.Invoice) (InvoiceResponse, error) {
	opts, err := s.PayOptions(ctx, q, inv.OrgID)
	if err != nil {
		return InvoiceResponse{}, err
	}

	contact, err := core.GetContact(ctx, q, inv.OrgID, inv.ContactID)
	if err != nil {
		return InvoiceResponse{}, err
	}

	var paid int64
	if err := q.QueryRow(ctx,
		`SELECT coalesce(sum(amount), 0) FROM payments WHERE org_id = $1 AND invoice_id = $2`,
		inv.OrgID, inv.ID,
	).Scan(&paid); err != nil {
		return InvoiceResponse{}, err
	}
	// Outstanding is floored at 0, matching the dashboard's own rule: an
	// overpaid invoice (allowed - see MatchInvoice) must never report a
	// negative balance owed.
	outstanding := max(int64(inv.Total)-paid, 0)

	resp := InvoiceResponse{
		ID: inv.ID, ContactID: inv.ContactID, ContactName: contact.Name, Number: inv.Number, Source: inv.Source, Status: inv.Status,
		Currency: inv.Currency, Subtotal: int64(inv.Subtotal), GSTTotal: int64(inv.GSTTotal), Total: int64(inv.Total),
		AmountPaid: paid, Outstanding: outstanding,
		PaymentReference: inv.PaymentReference, PayOptions: InvoicePayOptions(inv, opts), Lines: make([]LineResponse, len(inv.Lines)),
	}
	if inv.IssueDate != nil {
		s := inv.IssueDate.Format(dateLayout)
		resp.IssueDate = &s
	}
	if inv.DueDate != nil {
		s := inv.DueDate.Format(dateLayout)
		resp.DueDate = &s
	}
	for i, l := range inv.Lines {
		resp.Lines[i] = LineResponse{
			ID: l.ID, Description: l.Description, QuantityMilli: l.QuantityMilli, UnitAmount: int64(l.UnitAmount),
			GSTCode: string(l.GSTCode), GSTAmount: int64(l.GSTAmount), Amount: int64(l.Amount),
		}
	}
	return resp, nil
}

// PaymentSummary is one payment as shown on an invoice's detail page.
// provider_ref is deliberately left out - it's an internal reconciliation
// key, not something the web UI needs.
type PaymentSummary struct {
	ID     int64  `json:"id"`
	Amount int64  `json:"amount"`
	PaidOn string `json:"paid_on"`
	Method string `json:"method"`
}

// InvoiceDetailResponse is GET /invoices/{id}'s response shape: an
// InvoiceResponse plus the fields only the single-invoice detail view
// needs - the payments list and the hosted Stripe link (empty if none).
// List responses (GET /invoices) use InvoiceResponse directly and never
// carry these.
type InvoiceDetailResponse struct {
	InvoiceResponse
	Payments    []PaymentSummary `json:"payments"`
	ProviderURL string           `json:"provider_url"`
}

// InvoiceDetailJSON builds inv's detail-view API representation: the same
// fields as InvoiceJSON, plus its payments and provider URL.
func (s *Service) InvoiceDetailJSON(ctx context.Context, q core.Querier, inv core.Invoice) (InvoiceDetailResponse, error) {
	resp, err := s.InvoiceJSON(ctx, q, inv)
	if err != nil {
		return InvoiceDetailResponse{}, err
	}

	payments, err := core.ListPayments(ctx, q, inv.OrgID, inv.ID)
	if err != nil {
		return InvoiceDetailResponse{}, err
	}
	summaries := make([]PaymentSummary, len(payments))
	for i, p := range payments {
		summaries[i] = PaymentSummary{ID: p.ID, Amount: int64(p.Amount), PaidOn: p.PaidOn.Format(dateLayout), Method: p.Method}
	}

	return InvoiceDetailResponse{InvoiceResponse: resp, Payments: summaries, ProviderURL: inv.ProviderURL}, nil
}
