package property

import (
	"context"
	"fmt"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/reports"
)

// BillLine is one expense line tagged to a property: a bill the owner
// paid. Bills are ordinary expenses; this is a property-filtered view of
// them, never a separate record.
type BillLine struct {
	ExpenseID     int64
	LineID        int64
	PropertyID    int64
	SpentOn       time.Time
	Supplier      string
	Description   string
	Category      string
	Capital       bool
	Label         *core.Item21Label
	Code          core.GSTCode
	Incl, GST, Ex core.Cents
	ReceiptFileID *int64
	ReceiptName   string
}

// Bills returns orgID's property-tagged expense lines spent in [from, to],
// for propertyID or (nil) every property, by date.
func Bills(ctx context.Context, q core.Querier, orgID int64, propertyID *int64, from, to time.Time) ([]BillLine, error) {
	rows, err := q.Query(ctx, `
		SELECT e.id, el.id, el.property_id, e.spent_on, coalesce(s.name, ''), e.description,
			c.name, c.capital, c.item21_label, el.gst_code, el.amount, el.gst_amount,
			e.receipt_file_id, coalesce(f.original_name, '')
		FROM expense_lines el
		JOIN expenses e ON e.id = el.expense_id
		JOIN categories c ON c.id = el.category_id
		LEFT JOIN contacts s ON s.id = e.supplier_contact_id
		LEFT JOIN files f ON f.id = e.receipt_file_id
		WHERE el.org_id = $1 AND el.property_id IS NOT NULL
		  AND ($2::bigint IS NULL OR el.property_id = $2)
		  AND e.spent_on BETWEEN $3 AND $4
		ORDER BY e.spent_on, el.id`,
		orgID, propertyID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BillLine{}
	for rows.Next() {
		var b BillLine
		var label *string
		var code string
		var amount, gst int64
		if err := rows.Scan(&b.ExpenseID, &b.LineID, &b.PropertyID, &b.SpentOn, &b.Supplier, &b.Description,
			&b.Category, &b.Capital, &label, &code, &amount, &gst, &b.ReceiptFileID, &b.ReceiptName); err != nil {
			return nil, err
		}
		if label != nil {
			l := core.Item21Label(*label)
			b.Label = &l
		}
		b.Code = core.GSTCode(code)
		b.Incl, b.GST = core.Cents(amount), core.Cents(gst)
		b.Ex = b.Incl - b.GST
		out = append(out, b)
	}
	return out, rows.Err()
}

// Item21Result is one property's rental schedule for a financial year.
type Item21Result struct {
	GrossRent   core.Cents
	OtherIncome core.Cents
	Deductions  map[core.Item21Label]core.Cents
	Warnings    []string
}

// TotalDeductions sums r's deductions.
func (r Item21Result) TotalDeductions() core.Cents {
	var total core.Cents
	for _, v := range r.Deductions {
		total += v
	}
	return total
}

// rentIncomeCategory is reports.IncomeRow.Category for rent invoices.
const rentIncomeCategory = "Rent"

// Item21 rolls one property's cash-basis income, bills and depreciation
// for financial year fy up to the rental schedule, all ex-GST. Rent
// invoices are gross rent; other property income is other rental income.
// Capital bill lines are never deductions (they're claimed through
// depreciation); unmapped categories count as sundry with one warning per
// category.
func Item21(income []reports.IncomeRow, bills []BillLine, dep []DepreciationItem, fy int) Item21Result {
	r := Item21Result{Deductions: map[core.Item21Label]core.Cents{}}
	for _, row := range income {
		if row.Category == rentIncomeCategory {
			r.GrossRent += row.Ex
		} else {
			r.OtherIncome += row.Ex
		}
	}
	warned := map[string]bool{}
	for _, b := range bills {
		if b.Capital {
			continue
		}
		label := core.Item21Sundry
		if b.Label != nil {
			label = *b.Label
		} else if !warned[b.Category] {
			warned[b.Category] = true
			r.Warnings = append(r.Warnings, fmt.Sprintf("Category %q has no Item 21 label; counted as Sundry", b.Category))
		}
		r.Deductions[label] += b.Ex
	}
	for _, item := range dep {
		amount := item.AmountFor(fy)
		if amount == 0 {
			continue
		}
		if item.Type == "capital_works" {
			r.Deductions[core.Item21CapitalWorks] += amount
		} else {
			r.Deductions[core.Item21CapitalAllowances] += amount
		}
	}
	for label, v := range r.Deductions {
		if v == 0 {
			delete(r.Deductions, label)
		}
	}
	return r
}

// rentalProperty is one property's inputs to the rental reports.
type rentalProperty struct {
	Property     Property
	Income       []reports.IncomeRow
	Bills        []BillLine
	Depreciation []DepreciationItem
}

// loadRental gathers the rental-report inputs for financial year fy, for
// propertyID or (nil) every property in orgID, in property id order.
func loadRental(ctx context.Context, q core.Querier, orgID int64, propertyID *int64, fy int) ([]rentalProperty, time.Time, time.Time, error) {
	month, err := reports.OrgFYStartMonth(ctx, q, orgID)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	from, to, err := reports.FinancialYear(fy, month)
	if err != nil {
		return nil, from, to, fmt.Errorf("%w: %v", core.ErrInvalidInput, err)
	}

	var props []Property
	if propertyID != nil {
		p, err := GetProperty(ctx, q, orgID, *propertyID)
		if err != nil {
			return nil, from, to, err
		}
		props = []Property{p}
	} else if props, err = ListProperties(ctx, q, orgID); err != nil {
		return nil, from, to, err
	}

	income, err := reports.IncomeRows(ctx, q, orgID, from, to)
	if err != nil {
		return nil, from, to, err
	}
	bills, err := Bills(ctx, q, orgID, propertyID, from, to)
	if err != nil {
		return nil, from, to, err
	}
	dep, err := listDepreciation(ctx, q, orgID, propertyID, nil)
	if err != nil {
		return nil, from, to, err
	}

	index := map[int64]int{}
	out := make([]rentalProperty, len(props))
	for i, p := range props {
		out[i].Property = p
		index[p.ID] = i
	}
	for _, row := range income {
		if row.PropertyID == nil {
			continue
		}
		if i, ok := index[*row.PropertyID]; ok {
			out[i].Income = append(out[i].Income, row)
		}
	}
	for _, b := range bills {
		if i, ok := index[b.PropertyID]; ok {
			out[i].Bills = append(out[i].Bills, b)
		}
	}
	for _, d := range dep {
		if i, ok := index[d.PropertyID]; ok {
			out[i].Depreciation = append(out[i].Depreciation, d)
		}
	}
	return out, from, to, nil
}

// orderedDeductions returns r's deduction labels in rental-schedule order.
func orderedDeductions(r Item21Result) []core.Item21Label {
	out := []core.Item21Label{}
	for _, l := range core.Item21Labels {
		if _, ok := r.Deductions[l]; ok {
			out = append(out, l)
		}
	}
	return out
}

// categoryTotals sums bills' ex-GST amounts by category.
func categoryTotals(bills []BillLine) map[string]core.Cents {
	totals := map[string]core.Cents{}
	for _, b := range bills {
		totals[b.Category] += b.Ex
	}
	return totals
}

// formatDollars renders c as a plain decimal dollar amount ("1234.56").
func formatDollars(c core.Cents) string {
	sign := ""
	v := int64(c)
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}
