package recon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/timesheets"
)

// maxExpenseLineAmount is the largest amount (in cents) an expense line may
// carry, matching the ceiling invoice lines already use.
const maxExpenseLineAmount = 100_000_000_000

// residentialGSTWarning is added whenever a line is tagged to a residential
// property: no GST credit is claimed on residential property expenses, so
// the line is forced to INP regardless of its category's default or an
// explicit gst_code.
const residentialGSTWarning = "residential property expenses are input-taxed; GST credit not claimed"

// NewExpense is the input to CreateExpense.
type NewExpense struct {
	SpentOn           time.Time
	SupplierContactID *int64
	Description       string
	ReceiptFileID     *int64
	Lines             []NewExpenseLine
}

// NewExpenseLine is one line of a NewExpense. A nil GSTCode means "use the
// category's default". PropertyID and ProjectID are mutually exclusive.
type NewExpenseLine struct {
	CategoryID int64
	Amount     core.Cents
	GSTCode    *core.GSTCode
	PropertyID *int64
	ProjectID  *int64
}

// ExpenseLine is a stored, resolved expense line: its GST code is always
// set (the category default or the caller's override, forced to INP for a
// residential property) and its GST amount is already calculated.
type ExpenseLine struct {
	ID         int64
	OrgID      int64
	ExpenseID  int64
	CategoryID int64
	Amount     core.Cents
	GSTCode    core.GSTCode
	GSTAmount  core.Cents
	PropertyID *int64
	ProjectID  *int64
}

// Expense is a (possibly split-category) business expense, optionally tied
// to a receipt file and/or a reconciled bank line.
type Expense struct {
	ID                int64
	OrgID             int64
	SpentOn           time.Time
	SupplierContactID *int64
	Description       string
	ReceiptFileID     *int64
	BankLineID        *int64
	CreatedAt         time.Time
	Lines             []ExpenseLine
}

// ExpenseFilter narrows ListExpenses. A nil/false field means "don't filter
// on this". CategoryID, PropertyID and ProjectID match against any of an
// expense's lines, since those are per-line, not per-expense.
type ExpenseFilter struct {
	From, To                          *time.Time
	CategoryID, PropertyID, ProjectID *int64
	Unlinked                          bool
}

// CreateExpense validates in and inserts an expense with its lines for
// orgID, returning any non-fatal warnings alongside the created expense
// (e.g. a residential property line forced to INP). It returns
// core.ErrInvalidInput if in has no lines, or if any referenced category,
// contact, receipt file, property or project doesn't belong to orgID.
func CreateExpense(ctx context.Context, q core.Querier, orgID int64, in NewExpense) (Expense, []string, error) {
	if len(in.Lines) == 0 {
		return Expense{}, nil, fmt.Errorf("%w: expense must have at least one line", core.ErrInvalidInput)
	}
	if in.Description == "" {
		return Expense{}, nil, fmt.Errorf("%w: description is required", core.ErrInvalidInput)
	}
	if in.SpentOn.IsZero() {
		return Expense{}, nil, fmt.Errorf("%w: spent_on is required", core.ErrInvalidInput)
	}

	if in.SupplierContactID != nil {
		if _, err := core.GetContact(ctx, q, orgID, *in.SupplierContactID); err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return Expense{}, nil, fmt.Errorf("%w: supplier_contact_id not found", core.ErrInvalidInput)
			}
			return Expense{}, nil, err
		}
	}
	if in.ReceiptFileID != nil {
		if _, err := GetFile(ctx, q, orgID, *in.ReceiptFileID); err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return Expense{}, nil, fmt.Errorf("%w: receipt_file_id not found", core.ErrInvalidInput)
			}
			return Expense{}, nil, err
		}
	}

	settings, err := core.GetSettings(ctx, q, orgID)
	if err != nil {
		return Expense{}, nil, err
	}

	var warnings []string
	var anyResidential bool
	resolved := make([]ExpenseLine, len(in.Lines))
	for i, l := range in.Lines {
		if l.Amount <= 0 || int64(l.Amount) > maxExpenseLineAmount {
			return Expense{}, nil, fmt.Errorf("%w: line %d: amount must be from 1 to %d", core.ErrInvalidInput, i, maxExpenseLineAmount)
		}
		if l.PropertyID != nil && l.ProjectID != nil {
			return Expense{}, nil, fmt.Errorf("%w: line %d: set at most one of property_id or project_id", core.ErrInvalidInput, i)
		}

		cat, err := GetCategory(ctx, q, orgID, l.CategoryID)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return Expense{}, nil, fmt.Errorf("%w: line %d: category_id not found", core.ErrInvalidInput, i)
			}
			return Expense{}, nil, err
		}
		if cat.Type != "expense" {
			return Expense{}, nil, fmt.Errorf("%w: line %d: category_id must be an expense category", core.ErrInvalidInput, i)
		}

		var residential bool
		if l.PropertyID != nil {
			prop, err := property.GetProperty(ctx, q, orgID, *l.PropertyID)
			if err != nil {
				if errors.Is(err, core.ErrNotFound) {
					return Expense{}, nil, fmt.Errorf("%w: line %d: property_id not found", core.ErrInvalidInput, i)
				}
				return Expense{}, nil, err
			}
			residential = prop.Residential
		}
		if l.ProjectID != nil {
			if _, err := timesheets.GetProject(ctx, q, orgID, *l.ProjectID); err != nil {
				if errors.Is(err, core.ErrNotFound) {
					return Expense{}, nil, fmt.Errorf("%w: line %d: project_id not found", core.ErrInvalidInput, i)
				}
				return Expense{}, nil, err
			}
		}

		code := cat.DefaultGSTCode
		if l.GSTCode != nil {
			code = *l.GSTCode
		}
		if code != "" {
			if _, err := core.ParseGSTCode(string(code)); err != nil {
				return Expense{}, nil, fmt.Errorf("%w: line %d: %v", core.ErrInvalidInput, i, err)
			}
		}
		if residential {
			code = core.INP
			anyResidential = true
		}
		if code == "" {
			return Expense{}, nil, fmt.Errorf("%w: line %d: gst_code required; category has no default", core.ErrInvalidInput, i)
		}

		var gstAmount core.Cents
		if settings.GSTRegistered {
			gstAmount = core.GSTInInclusive(l.Amount, code)
		}

		resolved[i] = ExpenseLine{
			CategoryID: l.CategoryID, Amount: l.Amount, GSTCode: code, GSTAmount: gstAmount,
			PropertyID: l.PropertyID, ProjectID: l.ProjectID,
		}
	}
	// One warning per expense, not one per residential line, so a
	// multi-line expense with several residential-tagged lines doesn't
	// repeat the same text.
	if anyResidential {
		warnings = append(warnings, residentialGSTWarning)
	}

	exp := Expense{
		OrgID: orgID, SpentOn: in.SpentOn, SupplierContactID: in.SupplierContactID,
		Description: in.Description, ReceiptFileID: in.ReceiptFileID,
	}
	if err := q.QueryRow(ctx, `
		INSERT INTO expenses (org_id, spent_on, supplier_contact_id, description, receipt_file_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		orgID, in.SpentOn, in.SupplierContactID, in.Description, in.ReceiptFileID,
	).Scan(&exp.ID, &exp.CreatedAt); err != nil {
		return Expense{}, nil, err
	}

	for _, l := range resolved {
		l.OrgID = orgID
		l.ExpenseID = exp.ID
		if err := q.QueryRow(ctx, `
			INSERT INTO expense_lines (org_id, expense_id, category_id, amount, gst_code, gst_amount, property_id, project_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id`,
			orgID, exp.ID, l.CategoryID, int64(l.Amount), string(l.GSTCode), int64(l.GSTAmount), l.PropertyID, l.ProjectID,
		).Scan(&l.ID); err != nil {
			return Expense{}, nil, err
		}
		exp.Lines = append(exp.Lines, l)
	}

	if warnings == nil {
		warnings = []string{}
	}
	return exp, warnings, nil
}

const expenseColumns = `id, org_id, spent_on, supplier_contact_id, description, receipt_file_id, bank_line_id, created_at`

func scanExpense(row pgx.Row) (Expense, error) {
	var e Expense
	err := row.Scan(&e.ID, &e.OrgID, &e.SpentOn, &e.SupplierContactID, &e.Description, &e.ReceiptFileID, &e.BankLineID, &e.CreatedAt)
	return e, err
}

const expenseLineColumns = `id, org_id, expense_id, category_id, amount, gst_code, gst_amount, property_id, project_id`

func scanExpenseLine(row pgx.Row) (ExpenseLine, error) {
	var l ExpenseLine
	var amount, gstAmount int64
	var code string
	err := row.Scan(&l.ID, &l.OrgID, &l.ExpenseID, &l.CategoryID, &amount, &code, &gstAmount, &l.PropertyID, &l.ProjectID)
	l.Amount = core.Cents(amount)
	l.GSTCode = core.GSTCode(code)
	l.GSTAmount = core.Cents(gstAmount)
	return l, err
}

func listExpenseLines(ctx context.Context, q core.Querier, orgID, expenseID int64) ([]ExpenseLine, error) {
	rows, err := q.Query(ctx, `SELECT `+expenseLineColumns+` FROM expense_lines WHERE org_id = $1 AND expense_id = $2 ORDER BY id`, orgID, expenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lines := []ExpenseLine{}
	for rows.Next() {
		l, err := scanExpenseLine(rows)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// GetExpense looks up an expense (with its lines) by id, scoped to orgID.
// It returns core.ErrNotFound if no such expense exists in that org.
func GetExpense(ctx context.Context, q core.Querier, orgID, id int64) (Expense, error) {
	row := q.QueryRow(ctx, `SELECT `+expenseColumns+` FROM expenses WHERE org_id = $1 AND id = $2`, orgID, id)
	e, err := scanExpense(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Expense{}, core.ErrNotFound
	}
	if err != nil {
		return Expense{}, err
	}
	lines, err := listExpenseLines(ctx, q, orgID, e.ID)
	if err != nil {
		return Expense{}, err
	}
	e.Lines = lines
	return e, nil
}

// ListExpenses lists orgID's expenses matching filter, each with its
// lines.
func ListExpenses(ctx context.Context, q core.Querier, orgID int64, filter ExpenseFilter) ([]Expense, error) {
	sql := `SELECT DISTINCT e.id, e.org_id, e.spent_on, e.supplier_contact_id, e.description, e.receipt_file_id, e.bank_line_id, e.created_at FROM expenses e`
	needsLineJoin := filter.CategoryID != nil || filter.PropertyID != nil || filter.ProjectID != nil
	if needsLineJoin {
		sql += ` JOIN expense_lines l ON l.expense_id = e.id`
	}
	sql += ` WHERE e.org_id = $1`
	args := []any{orgID}
	if filter.From != nil {
		args = append(args, *filter.From)
		sql += fmt.Sprintf(" AND e.spent_on >= $%d", len(args))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		sql += fmt.Sprintf(" AND e.spent_on <= $%d", len(args))
	}
	if filter.CategoryID != nil {
		args = append(args, *filter.CategoryID)
		sql += fmt.Sprintf(" AND l.category_id = $%d", len(args))
	}
	if filter.PropertyID != nil {
		args = append(args, *filter.PropertyID)
		sql += fmt.Sprintf(" AND l.property_id = $%d", len(args))
	}
	if filter.ProjectID != nil {
		args = append(args, *filter.ProjectID)
		sql += fmt.Sprintf(" AND l.project_id = $%d", len(args))
	}
	if filter.Unlinked {
		sql += " AND e.bank_line_id IS NULL"
	}
	sql += " ORDER BY e.spent_on, e.id"

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	expenses := []Expense{}
	for rows.Next() {
		e, err := scanExpense(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		expenses = append(expenses, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range expenses {
		lines, err := listExpenseLines(ctx, q, orgID, expenses[i].ID)
		if err != nil {
			return nil, err
		}
		expenses[i].Lines = lines
	}
	return expenses, nil
}

// DeleteExpense deletes orgID's expense id (cascading to its lines) and
// audits what was deleted, with billing.ActorFrom(ctx) as the actor. The
// expense row is locked FOR UPDATE first, so a concurrent MatchExpense
// either links it before this sees it (409) or finds it gone (404) - never
// a deleted expense behind a "matched" line. It returns core.ErrNotFound
// if no such expense exists in that org, or core.ErrInvalidState (409,
// with a core.PublicError message) if the expense is linked to a bank
// line or is a payout's Stripe fee expense.
func DeleteExpense(ctx context.Context, q core.Querier, orgID, id int64) error {
	var bankLineID *int64
	err := q.QueryRow(ctx, `SELECT bank_line_id FROM expenses WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&bankLineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	if err != nil {
		return err
	}
	if bankLineID != nil {
		return &core.PublicError{Err: core.ErrInvalidState, Msg: "expense is linked to a bank line; unmatch the line first"}
	}
	var isFeeExpense bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payouts WHERE org_id = $1 AND fee_expense_id = $2)`, orgID, id).Scan(&isFeeExpense); err != nil {
		return err
	}
	if isFeeExpense {
		return &core.PublicError{Err: core.ErrInvalidState, Msg: "this is a Stripe payout fee; unmatch the payout instead"}
	}

	exp, err := GetExpense(ctx, q, orgID, id)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM expenses WHERE org_id = $1 AND id = $2`, orgID, id); err != nil {
		return err
	}

	lines := make([]map[string]any, len(exp.Lines))
	for i, l := range exp.Lines {
		lines[i] = map[string]any{
			"category_id": l.CategoryID, "amount": int64(l.Amount),
			"gst_code": string(l.GSTCode), "gst_amount": int64(l.GSTAmount),
		}
	}
	return core.Audit(ctx, q, orgID, billing.ActorFrom(ctx), "expense", id, "delete", map[string]any{
		"spent_on": exp.SpentOn.Format("2006-01-02"), "description": exp.Description, "lines": lines,
	})
}
