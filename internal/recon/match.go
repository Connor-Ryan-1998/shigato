// match.go implements reconciliation: matching bank lines to invoices
// (via a recorded payment) or expenses, ignoring lines that aren't
// business transactions, unmatching a previous decision, suggesting
// likely matches, and auto-matching unambiguous credits.
package recon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

// errAmbiguous is AutoMatch's internal signal that a line was skipped
// (already matched by a concurrent request, or fit zero/more-than-one
// candidate invoice) rather than a genuine failure. It never escapes
// AutoMatch.
var errAmbiguous = errors.New("ambiguous or already matched")

// Suggestion is one candidate match for a bank line, ranked by Score
// (higher is better).
type Suggestion struct {
	Kind   string // "invoice" or "expense"
	ID     int64
	Score  int
	Reason string
}

var validIgnoreReasons = map[string]bool{
	"transfer":       true,
	"drawings":       true,
	"bond_lodgement": true,
	"bond_refund":    true,
	"other":          true,
}

// lockBankLine locks orgID's bank line id FOR UPDATE and returns it. It
// returns core.ErrNotFound if no such line exists in that org.
func lockBankLine(ctx context.Context, tx core.Querier, orgID, id int64) (BankLine, error) {
	row := tx.QueryRow(ctx, `SELECT `+bankLineColumns+` FROM bank_lines WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id)
	l, err := scanBankLine(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return BankLine{}, core.ErrNotFound
	}
	return l, err
}

// getBankLine looks up orgID's bank line id without locking it, for the
// read-only Suggest path. It returns core.ErrNotFound if no such line
// exists in that org.
func getBankLine(ctx context.Context, q core.Querier, orgID, id int64) (BankLine, error) {
	row := q.QueryRow(ctx, `SELECT `+bankLineColumns+` FROM bank_lines WHERE org_id = $1 AND id = $2`, orgID, id)
	l, err := scanBankLine(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return BankLine{}, core.ErrNotFound
	}
	return l, err
}

// linkExpenseLine points expense expenseID at bank line lineID and marks
// the line matched. Called once the caller has already validated the
// amounts line up.
func linkExpenseLine(ctx context.Context, tx core.Querier, orgID, lineID, expenseID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE expenses SET bank_line_id = $1 WHERE org_id = $2 AND id = $3`, lineID, orgID, expenseID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE bank_lines SET status = 'matched' WHERE org_id = $1 AND id = $2`, orgID, lineID)
	return err
}

// MatchInvoice reconciles credit line lineID against invoiceID: it records
// a payment (amount = the line's amount, paid_on = the line's posted_on,
// linked to the bank line) and marks the line matched. The line must be
// unmatched and a credit (amount > 0); the invoice must be open. An
// amount above or below the invoice's outstanding balance is allowed -
// overpayment is stored as-is.
//
// The bank line is locked FOR UPDATE first, then the invoice (both inside
// tx), matching the lock order the rest of this package uses to avoid
// deadlocks against concurrent requests on the same two rows.
func MatchInvoice(ctx context.Context, tx core.Querier, orgID, lineID, invoiceID int64, method string) (core.Payment, error) {
	if method != "bank_transfer" && method != "payid" {
		return core.Payment{}, fmt.Errorf("%w: method must be bank_transfer or payid", core.ErrInvalidInput)
	}

	line, err := lockBankLine(ctx, tx, orgID, lineID)
	if err != nil {
		return core.Payment{}, err
	}
	if line.Status != "unmatched" {
		return core.Payment{}, core.ErrInvalidState
	}
	if line.Amount <= 0 {
		return core.Payment{}, fmt.Errorf("%w: bank line is not a credit", core.ErrInvalidInput)
	}

	// Lock and check the invoice ourselves (rather than relying solely on
	// RecordPayment's own lock) because RecordPayment only rejects
	// draft/void; a bank match additionally requires the invoice be open
	// - not already paid - so a stray reconciliation can't silently stack
	// a second payment onto a settled invoice.
	var invStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM invoices WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, invoiceID).Scan(&invStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Payment{}, core.ErrNotFound
	}
	if err != nil {
		return core.Payment{}, err
	}
	if invStatus != "open" {
		return core.Payment{}, core.ErrInvalidState
	}

	payment, _, err := core.RecordPayment(ctx, tx, orgID, core.NewPayment{
		InvoiceID: invoiceID,
		Amount:    line.Amount,
		PaidOn:    line.PostedOn,
		Method:    method,
	})
	if err != nil {
		return core.Payment{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE payments SET bank_line_id = $1 WHERE org_id = $2 AND id = $3`, lineID, orgID, payment.ID); err != nil {
		return core.Payment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE bank_lines SET status = 'matched' WHERE org_id = $1 AND id = $2`, orgID, lineID); err != nil {
		return core.Payment{}, err
	}

	if err := core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "match_invoice", map[string]any{
		"invoice_id": invoiceID, "payment_id": payment.ID, "method": method, "amount": int64(line.Amount),
	}); err != nil {
		return core.Payment{}, err
	}

	return payment, nil
}

// MatchExpense reconciles debit line lineID against an existing,
// not-yet-linked expense: -line.amount must equal the expense's total.
func MatchExpense(ctx context.Context, tx core.Querier, orgID, lineID, expenseID int64) error {
	line, err := lockBankLine(ctx, tx, orgID, lineID)
	if err != nil {
		return err
	}
	if line.Status != "unmatched" {
		return core.ErrInvalidState
	}
	if line.Amount >= 0 {
		return fmt.Errorf("%w: bank line is not a debit", core.ErrInvalidInput)
	}

	// Lock the expense row too (bank line first, then expense, per the
	// package's lock order) so two concurrent requests linking the same
	// expense to different lines can't both pass the "not already
	// linked" check before either commits.
	var expBankLineID *int64
	err = tx.QueryRow(ctx, `SELECT bank_line_id FROM expenses WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, expenseID).Scan(&expBankLineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	if err != nil {
		return err
	}
	if expBankLineID != nil {
		return fmt.Errorf("%w: expense is already linked to a bank line", core.ErrInvalidState)
	}

	exp, err := GetExpense(ctx, tx, orgID, expenseID)
	if err != nil {
		return err
	}

	var total core.Cents
	for _, l := range exp.Lines {
		total += l.Amount
	}
	if int64(total) != int64(-line.Amount) {
		return fmt.Errorf("%w: expense total must equal the bank line amount", core.ErrInvalidInput)
	}

	if err := linkExpenseLine(ctx, tx, orgID, lineID, expenseID); err != nil {
		return err
	}

	return core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "match_expense", map[string]any{
		"expense_id": expenseID,
	})
}

// CreateExpenseFromLine creates a new expense from in and links it to
// debit line lineID in one step. in.SpentOn defaults to the line's
// posted_on when left zero. The expense's total must equal -line.amount.
func CreateExpenseFromLine(ctx context.Context, tx core.Querier, orgID, lineID int64, in NewExpense) (Expense, []string, error) {
	line, err := lockBankLine(ctx, tx, orgID, lineID)
	if err != nil {
		return Expense{}, nil, err
	}
	if line.Status != "unmatched" {
		return Expense{}, nil, core.ErrInvalidState
	}
	if line.Amount >= 0 {
		return Expense{}, nil, fmt.Errorf("%w: bank line is not a debit", core.ErrInvalidInput)
	}

	if in.SpentOn.IsZero() {
		in.SpentOn = line.PostedOn
	}

	var total core.Cents
	for _, l := range in.Lines {
		total += l.Amount
	}
	if int64(total) != int64(-line.Amount) {
		return Expense{}, nil, fmt.Errorf("%w: expense total must equal the bank line amount", core.ErrInvalidInput)
	}

	exp, warnings, err := CreateExpense(ctx, tx, orgID, in)
	if err != nil {
		return Expense{}, nil, err
	}

	if err := linkExpenseLine(ctx, tx, orgID, lineID, exp.ID); err != nil {
		return Expense{}, nil, err
	}
	if err := core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "create_expense", map[string]any{
		"expense_id": exp.ID,
	}); err != nil {
		return Expense{}, nil, err
	}

	exp, err = GetExpense(ctx, tx, orgID, exp.ID)
	if err != nil {
		return Expense{}, nil, err
	}
	return exp, warnings, nil
}

// Ignore marks line lineID ignored with reason, one of transfer,
// drawings, bond_lodgement, bond_refund or other.
func Ignore(ctx context.Context, tx core.Querier, orgID, lineID int64, reason string) error {
	if !validIgnoreReasons[reason] {
		return fmt.Errorf("%w: reason must be one of transfer, drawings, bond_lodgement, bond_refund, other", core.ErrInvalidInput)
	}

	line, err := lockBankLine(ctx, tx, orgID, lineID)
	if err != nil {
		return err
	}
	if line.Status != "unmatched" {
		return core.ErrInvalidState
	}

	if _, err := tx.Exec(ctx, `UPDATE bank_lines SET status = 'ignored', ignore_reason = $1 WHERE org_id = $2 AND id = $3`, reason, orgID, lineID); err != nil {
		return err
	}
	return core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "ignore", map[string]any{"reason": reason})
}

// deletedPayment is one payment Unmatch deleted, recorded in its audit
// diff so a hard delete of money-moving rows leaves a traceable record of
// exactly what was removed.
type deletedPayment struct {
	ID        int64 `json:"id"`
	InvoiceID int64 `json:"invoice_id"`
	Amount    int64 `json:"amount"`
}

// Unmatch reverts a matched or ignored line back to unmatched. Only
// payments this module created (method bank_transfer or payid) are
// deleted - a payout link recorded by a later task's Stripe webhook flow
// is untouched. If deleting a payment leaves a previously-paid invoice
// below its total, that invoice is reopened. Any linked expense is
// unlinked, and any ignore reason is cleared. The audit diff records
// exactly what was deleted/reopened/unlinked, since this is the one
// action in the package that hard-deletes money-moving rows.
func Unmatch(ctx context.Context, tx core.Querier, orgID, lineID int64) error {
	line, err := lockBankLine(ctx, tx, orgID, lineID)
	if err != nil {
		return err
	}
	if line.Status == "unmatched" {
		return core.ErrInvalidState
	}

	rows, err := tx.Query(ctx, `
		DELETE FROM payments
		WHERE org_id = $1 AND bank_line_id = $2 AND method IN ('bank_transfer', 'payid')
		RETURNING id, invoice_id, amount`,
		orgID, lineID,
	)
	if err != nil {
		return err
	}
	var deleted []deletedPayment
	invoiceIDs := map[int64]bool{}
	for rows.Next() {
		var p deletedPayment
		if err := rows.Scan(&p.ID, &p.InvoiceID, &p.Amount); err != nil {
			rows.Close()
			return err
		}
		deleted = append(deleted, p)
		invoiceIDs[p.InvoiceID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	var reopened []int64
	for invID := range invoiceIDs {
		var status string
		var total int64
		err := tx.QueryRow(ctx, `SELECT status, total FROM invoices WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, invID).Scan(&status, &total)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if status != "paid" {
			continue
		}
		var sum int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0) FROM payments WHERE org_id = $1 AND invoice_id = $2`, orgID, invID).Scan(&sum); err != nil {
			return err
		}
		if sum < total {
			if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'open' WHERE org_id = $1 AND id = $2`, orgID, invID); err != nil {
				return err
			}
			reopened = append(reopened, invID)
		}
	}

	var unlinkedExpenseID *int64
	var expID int64
	err = tx.QueryRow(ctx, `SELECT id FROM expenses WHERE org_id = $1 AND bank_line_id = $2`, orgID, lineID).Scan(&expID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// no linked expense
	case err != nil:
		return err
	default:
		unlinkedExpenseID = &expID
	}

	if _, err := tx.Exec(ctx, `UPDATE expenses SET bank_line_id = NULL WHERE org_id = $1 AND bank_line_id = $2`, orgID, lineID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bank_lines SET status = 'unmatched', ignore_reason = NULL WHERE org_id = $1 AND id = $2`, orgID, lineID); err != nil {
		return err
	}

	if deleted == nil {
		deleted = []deletedPayment{}
	}
	if reopened == nil {
		reopened = []int64{}
	}

	return core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "unmatch", map[string]any{
		"deleted_payments":       deleted,
		"reopened_invoice_ids":   reopened,
		"unlinked_expense_id":    unlinkedExpenseID,
		"previous_status":        line.Status,
		"previous_ignore_reason": line.IgnoreReason,
	})
}

// daysBetween returns the absolute number of whole days between a and b.
func daysBetween(a, b time.Time) int {
	d := int(b.Sub(a).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

// Suggest ranks candidate invoices (for a credit line) or expenses (for a
// debit line) that lineID might reconcile against, highest score first,
// capped at 10.
func Suggest(ctx context.Context, q core.Querier, orgID, lineID int64) ([]Suggestion, error) {
	line, err := getBankLine(ctx, q, orgID, lineID)
	if err != nil {
		return nil, err
	}

	var suggestions []Suggestion
	switch {
	case line.Amount > 0:
		rows, err := q.Query(ctx, `
			SELECT id, payment_reference, issue_date,
				total - coalesce((SELECT sum(amount) FROM payments p WHERE p.org_id = i.org_id AND p.invoice_id = i.id), 0) AS outstanding
			FROM invoices i
			WHERE org_id = $1 AND status = 'open'`,
			orgID,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var ref string
			var issueDate *time.Time
			var outstanding int64
			if err := rows.Scan(&id, &ref, &issueDate, &outstanding); err != nil {
				return nil, err
			}
			if core.Cents(outstanding) != line.Amount {
				continue
			}
			score := 100
			reason := "payment reference matches"
			if !strings.Contains(strings.ToLower(line.Description), strings.ToLower(ref)) {
				score = 50
				reason = "amount matches"
				if issueDate != nil {
					score -= daysBetween(*issueDate, line.PostedOn)
				}
				if score < 0 {
					score = 0
				}
			}
			suggestions = append(suggestions, Suggestion{Kind: "invoice", ID: id, Score: score, Reason: reason})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	case line.Amount < 0:
		rows, err := q.Query(ctx, `
			SELECT e.id, e.spent_on, coalesce(sum(l.amount), 0) AS total
			FROM expenses e
			JOIN expense_lines l ON l.expense_id = e.id
			WHERE e.org_id = $1 AND e.bank_line_id IS NULL
			GROUP BY e.id, e.spent_on`,
			orgID,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var spentOn time.Time
			var total int64
			if err := rows.Scan(&id, &spentOn, &total); err != nil {
				return nil, err
			}
			if core.Cents(total) != -line.Amount {
				continue
			}
			days := daysBetween(spentOn, line.PostedOn)
			if days > 7 {
				continue
			}
			score := 50 - days
			if score < 0 {
				score = 0
			}
			suggestions = append(suggestions, Suggestion{Kind: "expense", ID: id, Score: score, Reason: "amount and date match"})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	sort.SliceStable(suggestions, func(i, j int) bool { return suggestions[i].Score > suggestions[j].Score })
	if len(suggestions) > 10 {
		suggestions = suggestions[:10]
	}
	return suggestions, nil
}

// autoMatchCandidates returns the ids of open invoices whose
// payment_reference appears (case-insensitively) in line's description and
// whose outstanding balance equals line's amount.
func autoMatchCandidates(ctx context.Context, tx core.Querier, orgID int64, line BankLine) ([]int64, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM invoices i
		WHERE org_id = $1 AND status = 'open'
			AND position(upper(payment_reference) in upper($2)) > 0
			AND (total - coalesce((SELECT sum(amount) FROM payments p WHERE p.org_id = i.org_id AND p.invoice_id = i.id), 0)) = $3`,
		orgID, line.Description, int64(line.Amount),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AutoMatch matches every unmatched credit line in orgID that fits exactly
// one open invoice (by payment reference and outstanding amount), each in
// its own transaction so one line's failure can't roll back another's
// success. A line already matched by a concurrent request, or one that
// fits zero or more than one invoice, is skipped and not counted.
func AutoMatch(ctx context.Context, pool *pgxpool.Pool, orgID int64) (int, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM bank_lines WHERE org_id = $1 AND status = 'unmatched' AND amount > 0 ORDER BY id`, orgID)
	if err != nil {
		return 0, err
	}
	var lineIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		lineIDs = append(lineIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	matched := 0
	for _, lineID := range lineIDs {
		err := module.RunInTx(ctx, pool, func(tx pgx.Tx) error {
			line, err := lockBankLine(ctx, tx, orgID, lineID)
			if err != nil {
				return err
			}
			if line.Status != "unmatched" {
				return errAmbiguous
			}
			candidates, err := autoMatchCandidates(ctx, tx, orgID, line)
			if err != nil {
				return err
			}
			if len(candidates) != 1 {
				return errAmbiguous
			}
			_, err = MatchInvoice(ctx, tx, orgID, lineID, candidates[0], "bank_transfer")
			return err
		})
		if err != nil {
			// errAmbiguous is this loop's own skip signal; ErrInvalidState
			// from MatchInvoice means the candidate invoice (checked
			// un-locked by autoMatchCandidates) was paid or voided by a
			// concurrent request between that check and MatchInvoice's own
			// lock - also a skip, not a reason to abort the whole run.
			if errors.Is(err, errAmbiguous) || errors.Is(err, core.ErrInvalidState) {
				continue
			}
			return matched, err
		}
		matched++
	}
	return matched, nil
}
