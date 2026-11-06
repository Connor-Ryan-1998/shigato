// Package portal implements the contact-scoped portal API: what a signed-in
// tenant or client may see of their own org - their invoices and, for
// tenants, their leases. Every query here is scoped by both org_id and the
// caller's own contact_id (global-constraints.md "Portal isolation"); none
// of it is reachable without a tenant/client membership, enforced by
// auth.Auth.RequirePortal before these functions ever run.
package portal

import (
	"context"
	"time"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/property"
)

// portalInvoiceStatuses are the only invoice statuses the portal ever shows
// (global-constraints.md "Portal isolation": draft and void are never
// shown).
var portalInvoiceStatuses = []string{"open", "paid", "uncollectible"}

// Org is one org a portal user belongs to, as listed by GET
// /api/v1/portal/orgs.
type Org struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// ListOrgs returns every org userID holds a tenant or client membership in
// - a portal user with memberships in multiple orgs sees them all.
func ListOrgs(ctx context.Context, q core.Querier, userID int64) ([]Org, error) {
	rows, err := q.Query(ctx, `
		SELECT o.slug, o.name, m.role
		FROM memberships m
		JOIN orgs o ON o.id = m.org_id
		WHERE m.user_id = $1 AND m.role IN ('tenant', 'client')
		ORDER BY o.name`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orgs := []Org{}
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.Slug, &o.Name, &o.Role); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

// ListInvoices returns orgID's invoices addressed to contactID with a
// portal-visible status (open, paid or uncollectible - never draft or
// void), newest first, in the same billing.InvoiceResponse shape the staff
// invoice list uses.
func ListInvoices(ctx context.Context, q core.Querier, bill *billing.Service, orgID, contactID int64) ([]billing.InvoiceResponse, error) {
	rows, err := q.Query(ctx, `
		SELECT id FROM invoices
		WHERE org_id = $1 AND contact_id = $2 AND status = ANY($3)
		ORDER BY issue_date DESC NULLS FIRST, id DESC`,
		orgID, contactID, portalInvoiceStatuses,
	)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	invoices := make([]billing.InvoiceResponse, 0, len(ids))
	for _, id := range ids {
		inv, err := core.GetInvoice(ctx, q, orgID, id)
		if err != nil {
			return nil, err
		}
		resp, err := bill.InvoiceJSON(ctx, q, inv)
		if err != nil {
			return nil, err
		}
		invoices = append(invoices, resp)
	}
	return invoices, nil
}

// InvoiceDetail is GET /api/v1/portal/{org}/invoices/{id}'s response shape:
// the same detail an org's own staff see, plus the org's bank details -
// each shown only when its pay option is actually offered on this invoice.
type InvoiceDetail struct {
	billing.InvoiceDetailResponse
	BankBSB     string `json:"bank_bsb,omitempty"`
	BankAccount string `json:"bank_account,omitempty"`
	PayID       string `json:"payid,omitempty"`
}

// GetInvoiceDetail returns invoiceID's portal detail view, scoped to orgID
// and contactID: an invoice addressed to a different contact, a cross-org
// id, or a draft/void invoice all return core.ErrNotFound - the caller maps
// that to a 404, the same response whichever reason applies.
func GetInvoiceDetail(ctx context.Context, q core.Querier, bill *billing.Service, orgID, contactID, invoiceID int64) (InvoiceDetail, error) {
	inv, err := core.GetInvoice(ctx, q, orgID, invoiceID)
	if err != nil {
		return InvoiceDetail{}, err
	}
	if inv.ContactID != contactID || inv.Status == "draft" || inv.Status == "void" {
		return InvoiceDetail{}, core.ErrNotFound
	}

	resp, err := bill.InvoiceDetailJSON(ctx, q, inv)
	if err != nil {
		return InvoiceDetail{}, err
	}

	settings, err := core.GetSettings(ctx, q, orgID)
	if err != nil {
		return InvoiceDetail{}, err
	}

	detail := InvoiceDetail{InvoiceDetailResponse: resp}
	for _, opt := range resp.PayOptions {
		switch opt {
		case "bank_transfer":
			detail.BankBSB = settings.BankBSB
			detail.BankAccount = settings.BankAccount
		case "payid":
			detail.PayID = settings.PayID
		}
	}
	return detail, nil
}

// Lease is one of a tenant's leases, as listed by GET
// /api/v1/portal/{org}/leases.
type Lease struct {
	ID              int64              `json:"id"`
	PropertyName    string             `json:"property_name"`
	PropertyAddress string             `json:"property_address"`
	RentAmount      int64              `json:"rent_amount"`
	Frequency       string             `json:"frequency"`
	NextDue         *LeaseLedgerPeriod `json:"next_due,omitempty"`
	Arrears         int64              `json:"arrears"`
	InvoicedNotDue  int64              `json:"invoiced_not_due"`
}

// LeaseLedgerPeriod summarises the next rent period a tenant still owes
// something on.
type LeaseLedgerPeriod struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	AmountDue int64  `json:"amount_due"`
}

// currentRent returns leaseID's rent amount as of today: the latest rent
// change whose effective date isn't after today, falling back to the
// lease's first (initial) rent change if every change is still in the
// future (shouldn't happen in practice, since the first change is always
// dated the lease's start date).
func currentRent(l property.Lease, today time.Time) int64 {
	amount := int64(0)
	if len(l.RentChanges) > 0 {
		amount = int64(l.RentChanges[0].Amount)
	}
	for _, c := range l.RentChanges {
		if !c.EffectiveDate.After(today) {
			amount = int64(c.Amount)
		}
	}
	return amount
}

// nextDuePeriod returns the first (earliest) period in ledger that isn't
// yet fully paid, or nil if every period is settled.
func nextDuePeriod(ledger property.Ledger) *LeaseLedgerPeriod {
	for _, p := range ledger.Periods {
		if p.Paid < p.AmountDue {
			return &LeaseLedgerPeriod{
				Start: p.Start.Format("2006-01-02"), End: p.End.Format("2006-01-02"),
				AmountDue: int64(p.AmountDue),
			}
		}
	}
	return nil
}

// ListLeases returns orgID's leases where contactID is listed in
// lease_tenants, each with its property, current rent, next due period and
// ledger arrears/invoiced-not-due summary (property.GetLedger).
func ListLeases(ctx context.Context, q core.Querier, orgID, contactID int64, today time.Time) ([]Lease, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT l.id FROM leases l
		JOIN lease_tenants lt ON lt.lease_id = l.id
		WHERE l.org_id = $1 AND lt.contact_id = $2
		ORDER BY l.id`,
		orgID, contactID,
	)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	leases := make([]Lease, 0, len(ids))
	for _, id := range ids {
		l, err := property.GetLease(ctx, q, orgID, id)
		if err != nil {
			return nil, err
		}
		prop, err := property.GetProperty(ctx, q, orgID, l.PropertyID)
		if err != nil {
			return nil, err
		}
		ledger, err := property.GetLedger(ctx, q, orgID, id, today)
		if err != nil {
			return nil, err
		}

		leases = append(leases, Lease{
			ID:              l.ID,
			PropertyName:    prop.Name,
			PropertyAddress: prop.Address,
			RentAmount:      currentRent(l, today),
			Frequency:       string(l.Frequency),
			NextDue:         nextDuePeriod(ledger),
			Arrears:         int64(ledger.Arrears),
			InvoicedNotDue:  int64(ledger.InvoicedNotDue),
		})
	}
	return leases, nil
}
