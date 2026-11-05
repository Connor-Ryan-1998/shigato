package core

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// NewPayment describes a payment to record against an invoice.
type NewPayment struct {
	InvoiceID   int64
	Amount      Cents
	PaidOn      time.Time
	Method      string
	ProviderRef string
}

// Payment is a persisted payment.
type Payment struct {
	ID          int64
	OrgID       int64
	InvoiceID   int64
	Amount      Cents
	PaidOn      time.Time
	Method      string
	ProviderRef string
}

// RecordPayment locks the invoice row, rejects payments against draft or
// void invoices, and inserts the payment. If ProviderRef is non-empty and
// already recorded for this org, the insert is a no-op (ON CONFLICT DO
// NOTHING) and the existing payment is returned with created=false, so
// retried webhook/manual submissions are idempotent.
//
// After a successful insert, if the sum of the invoice's payments is at
// least its total, the invoice is marked 'paid'.
func RecordPayment(ctx context.Context, tx Querier, orgID int64, in NewPayment) (Payment, bool, error) {
	var status string
	var total int64
	err := tx.QueryRow(ctx,
		`SELECT status, total FROM invoices WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		orgID, in.InvoiceID,
	).Scan(&status, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, ErrNotFound
	}
	if err != nil {
		return Payment{}, false, err
	}
	if status == "draft" || status == "void" {
		return Payment{}, false, ErrInvalidState
	}

	var providerRef *string
	if in.ProviderRef != "" {
		providerRef = &in.ProviderRef
	}

	var p Payment
	var amount int64
	err = tx.QueryRow(ctx, `
		INSERT INTO payments (org_id, invoice_id, amount, paid_on, method, provider_ref)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id, provider_ref) DO NOTHING
		RETURNING id, org_id, invoice_id, amount, paid_on, method, coalesce(provider_ref,'')`,
		orgID, in.InvoiceID, int64(in.Amount), in.PaidOn, in.Method, providerRef,
	).Scan(&p.ID, &p.OrgID, &p.InvoiceID, &amount, &p.PaidOn, &p.Method, &p.ProviderRef)

	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		err = tx.QueryRow(ctx, `
			SELECT id, org_id, invoice_id, amount, paid_on, method, coalesce(provider_ref,'')
			FROM payments WHERE org_id = $1 AND provider_ref = $2`,
			orgID, in.ProviderRef,
		).Scan(&p.ID, &p.OrgID, &p.InvoiceID, &amount, &p.PaidOn, &p.Method, &p.ProviderRef)
	}
	if err != nil {
		return Payment{}, false, err
	}
	p.Amount = Cents(amount)

	if created {
		var sum int64
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(sum(amount),0) FROM payments WHERE org_id = $1 AND invoice_id = $2`,
			orgID, in.InvoiceID,
		).Scan(&sum); err != nil {
			return Payment{}, false, err
		}
		if sum >= total {
			if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'paid' WHERE org_id = $1 AND id = $2`, orgID, in.InvoiceID); err != nil {
				return Payment{}, false, err
			}
		}
	}

	return p, created, nil
}
