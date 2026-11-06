package reports

import (
	"context"
	"sort"
	"strconv"
	"time"

	"shigoto/internal/core"
)

// incomeCategoryBySource maps an invoice's source to the P&L/BAS income
// category it contributes to, per the Global Constraints.
var incomeCategoryBySource = map[string]string{
	"rent":      "Rent",
	"timesheet": "Consulting",
	"manual":    "Other income",
}

// IncomeRow is one invoice line's allocated share of a payment, counted on
// its payment's paid_on date (cash basis).
type IncomeRow struct {
	PaymentID     int64
	InvoiceID     int64
	InvoiceNumber *int
	PaidOn        time.Time
	Category      string
	Code          core.GSTCode
	Incl, GST, Ex core.Cents
	PropertyID    *int64
	ProjectID     *int64
}

// ExpenseRow is one expense line, counted on its expense's spent_on date
// (cash basis).
type ExpenseRow struct {
	ExpenseID     int64
	LineID        int64
	SpentOn       time.Time
	Category      string
	Capital       bool
	Code          core.GSTCode
	Incl, GST, Ex core.Cents
	PropertyID    *int64
	ProjectID     *int64
}

// IncomeRows returns the allocated income rows for orgID over [from, to]:
// payments dated in that inclusive range against invoices that are not
// draft, void, or a bond (bonds never appear in P&L/BAS - they're a
// liability, not income). Each payment is allocated across its invoice's
// lines via Allocate, giving one IncomeRow per line that received a
// nonzero-length share.
//
// This queries invoices/invoice_lines/payments directly rather than going
// through a billing or core helper that returns core.Invoice, so a single
// round trip per payment is enough; reports must not import
// internal/recon, internal/property or internal/timesheets, but core's own
// tables are fair game.
func IncomeRows(ctx context.Context, q core.Querier, orgID int64, from, to time.Time) ([]IncomeRow, error) {
	rows, err := q.Query(ctx, `
		SELECT p.id, p.invoice_id, i.number, p.paid_on, i.source, p.amount
		FROM payments p
		JOIN invoices i ON i.id = p.invoice_id
		WHERE i.org_id = $1 AND p.paid_on BETWEEN $2 AND $3
		  AND i.status NOT IN ('draft', 'void') AND i.source != 'bond'
		ORDER BY p.paid_on, p.id`,
		orgID, from, to,
	)
	if err != nil {
		return nil, err
	}
	type payment struct {
		id, invoiceID int64
		number        *int32
		paidOn        time.Time
		source        string
		amount        int64
	}
	var payments []payment
	for rows.Next() {
		var p payment
		if err := rows.Scan(&p.id, &p.invoiceID, &p.number, &p.paidOn, &p.source, &p.amount); err != nil {
			rows.Close()
			return nil, err
		}
		payments = append(payments, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []IncomeRow
	for _, p := range payments {
		lineRows, err := q.Query(ctx, `
			SELECT amount, gst_amount, gst_code, property_id, project_id
			FROM invoice_lines WHERE invoice_id = $1 AND org_id = $2 ORDER BY id`,
			p.invoiceID, orgID,
		)
		if err != nil {
			return nil, err
		}
		var lines []AllocLine
		for lineRows.Next() {
			var amount, gstAmount int64
			var code string
			var propertyID, projectID *int64
			if err := lineRows.Scan(&amount, &gstAmount, &code, &propertyID, &projectID); err != nil {
				lineRows.Close()
				return nil, err
			}
			lines = append(lines, AllocLine{
				Amount: core.Cents(amount), GST: core.Cents(gstAmount), Code: core.GSTCode(code),
				PropertyID: propertyID, ProjectID: projectID,
			})
		}
		lineRows.Close()
		if err := lineRows.Err(); err != nil {
			return nil, err
		}

		var number *int
		if p.number != nil {
			n := int(*p.number)
			number = &n
		}
		category := incomeCategoryBySource[p.source]

		for _, share := range Allocate(core.Cents(p.amount), lines) {
			out = append(out, IncomeRow{
				PaymentID: p.id, InvoiceID: p.invoiceID, InvoiceNumber: number, PaidOn: p.paidOn,
				Category: category, Code: share.Line.Code, Incl: share.Incl, GST: share.GST, Ex: share.Ex,
				PropertyID: share.Line.PropertyID, ProjectID: share.Line.ProjectID,
			})
		}
	}
	return out, nil
}

// ExpenseRows returns orgID's expense lines whose expense is spent_on in
// [from, to] (inclusive), one ExpenseRow per line.
func ExpenseRows(ctx context.Context, q core.Querier, orgID int64, from, to time.Time) ([]ExpenseRow, error) {
	rows, err := q.Query(ctx, `
		SELECT el.id, el.expense_id, e.spent_on, c.name, c.capital, el.gst_code, el.amount, el.gst_amount, el.property_id, el.project_id
		FROM expense_lines el
		JOIN expenses e ON e.id = el.expense_id
		JOIN categories c ON c.id = el.category_id
		WHERE el.org_id = $1 AND e.spent_on BETWEEN $2 AND $3
		ORDER BY e.spent_on, el.id`,
		orgID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ExpenseRow
	for rows.Next() {
		var r ExpenseRow
		var amount, gstAmount int64
		var code string
		if err := rows.Scan(&r.LineID, &r.ExpenseID, &r.SpentOn, &r.Category, &r.Capital, &code, &amount, &gstAmount, &r.PropertyID, &r.ProjectID); err != nil {
			return nil, err
		}
		r.Code = core.GSTCode(code)
		r.Incl = core.Cents(amount)
		r.GST = core.Cents(gstAmount)
		r.Ex = r.Incl - r.GST
		out = append(out, r)
	}
	return out, rows.Err()
}

// PnLGroup is one group's income and expenses, by category, ex-GST.
type PnLGroup struct {
	Key                             string
	Income, Expenses                map[string]core.Cents
	TotalIncome, TotalExpenses, Net core.Cents
}

// unassignedKey is a PnLGroup.Key for rows with neither a property nor a
// project.
const unassignedKey = "Unassigned"

// orgGroupKey is PnL's single group key for groupBy "org"; the caller (the
// HTTP handler, which has DB access PnL itself doesn't) substitutes the
// org's real name before returning the group to a client, per the P&L spec
// that the org grouping key is the org's name.
const orgGroupKey = "org"

// groupKey returns row's PnLGroup key for groupBy ("org", "property" or
// "project"). property/project keys are the raw id (as a string); like
// orgGroupKey, the HTTP handler resolves these to the property/project's
// name afterwards, since PnL itself has no DB access to look names up.
func groupKey(groupBy string, propertyID, projectID *int64) string {
	switch groupBy {
	case "property":
		if propertyID != nil {
			return strconv.FormatInt(*propertyID, 10)
		}
		return unassignedKey
	case "project":
		if projectID != nil {
			return strconv.FormatInt(*projectID, 10)
		}
		return unassignedKey
	default:
		return orgGroupKey
	}
}

func group(groups map[string]*PnLGroup, order *[]string, key string) *PnLGroup {
	g, ok := groups[key]
	if !ok {
		g = &PnLGroup{Key: key, Income: map[string]core.Cents{}, Expenses: map[string]core.Cents{}}
		groups[key] = g
		*order = append(*order, key)
	}
	return g
}

// PnL groups income and expense rows (ex-GST) by groupBy ("org", "property"
// or "project"); rows with neither a property nor a project fall into
// "Unassigned". Groups are returned sorted by Key for a deterministic
// response.
func PnL(income []IncomeRow, exp []ExpenseRow, groupBy string) []PnLGroup {
	groups := map[string]*PnLGroup{}
	var order []string

	for _, row := range income {
		g := group(groups, &order, groupKey(groupBy, row.PropertyID, row.ProjectID))
		g.Income[row.Category] += row.Ex
		g.TotalIncome += row.Ex
	}
	for _, row := range exp {
		g := group(groups, &order, groupKey(groupBy, row.PropertyID, row.ProjectID))
		g.Expenses[row.Category] += row.Ex
		g.TotalExpenses += row.Ex
	}

	sort.Strings(order)
	out := make([]PnLGroup, len(order))
	for i, key := range order {
		g := groups[key]
		g.Net = g.TotalIncome - g.TotalExpenses
		out[i] = *g
	}
	return out
}
