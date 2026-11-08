package property

import (
	"context"
	"time"

	"shigoto/internal/core"
)

const warnBilledPeriods = "saved change overlaps existing invoice periods; existing invoices were not recalculated; review the affected invoices"

type affectedInvoicePeriod struct {
	InvoiceID     int64  `json:"invoice_id"`
	InvoiceNumber *int   `json:"invoice_number,omitempty"`
	Status        string `json:"status"`
	Start         string `json:"start"`
	End           string `json:"end"`
}

// affectedInvoicePeriods is conservative date-overlap advice, not a recalculation.
func affectedInvoicePeriods(ctx context.Context, q core.Querier, orgID, leaseID int64, start time.Time, end *time.Time) ([]affectedInvoicePeriod, error) {
	rows, err := q.Query(ctx, `
		SELECT i.id, i.number, i.status, rp.period_start, rp.period_end
		FROM rent_periods rp
		JOIN invoices i ON i.org_id = rp.org_id AND i.id = rp.invoice_id
		WHERE rp.org_id = $1 AND rp.lease_id = $2 AND i.status <> 'void'
			AND rp.period_end >= $3::date
			AND ($4::date IS NULL OR rp.period_start <= $4::date)
		ORDER BY rp.period_start, i.id`, orgID, leaseID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var affected []affectedInvoicePeriod
	for rows.Next() {
		var item affectedInvoicePeriod
		var from, to time.Time
		if err := rows.Scan(&item.InvoiceID, &item.InvoiceNumber, &item.Status, &from, &to); err != nil {
			return nil, err
		}
		item.Start, item.End = from.Format(dateLayout), to.Format(dateLayout)
		affected = append(affected, item)
	}
	return affected, rows.Err()
}

func rentChangeAffectedInvoicePeriods(ctx context.Context, q core.Querier, orgID, leaseID int64, effective time.Time) ([]affectedInvoicePeriod, error) {
	var next *time.Time
	if err := q.QueryRow(ctx, `
		SELECT min(rc.effective_date) FROM lease_rent_changes rc
		JOIN leases l ON l.id = rc.lease_id
		WHERE l.org_id = $1 AND l.id = $2 AND rc.effective_date > $3`, orgID, leaseID, effective).Scan(&next); err != nil {
		return nil, err
	}
	var end *time.Time
	if next != nil {
		last := next.AddDate(0, 0, -1)
		end = &last
	}
	return affectedInvoicePeriods(ctx, q, orgID, leaseID, effective, end)
}
