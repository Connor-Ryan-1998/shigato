//go:build integration

package recon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/recon"
)

func createContact(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: "client", Name: "Test Contact"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

// testInvoice creates and issues a one-line, GST-free invoice totalling
// totalCents, returning it.
func testInvoice(t *testing.T, pool *pgxpool.Pool, orgID, contactID int64, totalCents int64) core.Invoice {
	t.Helper()
	ctx := context.Background()
	draft, err := core.CreateDraftInvoice(ctx, pool, orgID, core.NewInvoice{
		ContactID: contactID,
		Source:    "manual",
		DueInDays: 14,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: 1000, UnitAmount: core.Cents(totalCents), GSTCode: "N-T"},
		},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	inv, err := core.IssueInvoice(ctx, tx, orgID, draft.ID, time.Now())
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return inv
}

var bankLineSeq int64

// testBankLineID inserts a bank line for orgID directly (bypassing CSV
// import, which these tests don't need) and returns its id. Each call uses
// a fresh bank account and a unique dedupe key so repeated calls in the
// same test never collide.
func testBankLineID(t *testing.T, pool *pgxpool.Pool, orgID int64, amount int64, postedOn time.Time, description string) int64 {
	t.Helper()
	ctx := context.Background()
	bankLineSeq++
	seq := bankLineSeq

	acct := testBankAccount(t, pool, orgID, fmt.Sprintf("acct-%d", seq), commbankMapping())

	var importID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO bank_imports (org_id, bank_account_id, file_sha256, rows_total, rows_new)
		VALUES ($1, $2, $3, 0, 0) RETURNING id`,
		orgID, acct.ID, []byte(fmt.Sprintf("sha-%d", seq)),
	).Scan(&importID); err != nil {
		t.Fatalf("inserting bank_import: %v", err)
	}

	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO bank_lines (org_id, bank_account_id, import_id, posted_on, amount, description, dedupe_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		orgID, acct.ID, importID, postedOn, amount, description, []byte(fmt.Sprintf("dedupe-%d", seq)),
	).Scan(&id); err != nil {
		t.Fatalf("inserting bank_line: %v", err)
	}
	return id
}

func bankLineStatus(t *testing.T, pool *pgxpool.Pool, lineID int64) (status string, reason *string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT status, ignore_reason FROM bank_lines WHERE id = $1`, lineID).Scan(&status, &reason); err != nil {
		t.Fatalf("reading bank_line status: %v", err)
	}
	return status, reason
}

func countPayments(t *testing.T, pool *pgxpool.Pool, orgID, invoiceID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE org_id = $1 AND invoice_id = $2`, orgID, invoiceID).Scan(&n); err != nil {
		t.Fatalf("counting payments: %v", err)
	}
	return n
}

// TestMatchInvoicePaysAndUnmatchReverts covers the happy path end to end: a
// credit line matched to an invoice of the same total pays it off, and
// Unmatch reverses that cleanly - the invoice goes back to open and no
// payment remains.
func TestMatchInvoicePaysAndUnmatchReverts(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	inv := testInvoice(t, pool, org.ID, contactID, 50000)
	lineID := testBankLineID(t, pool, org.ID, 50000, mustDate(t, "2026-11-02"), "Transfer INV payment")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := recon.MatchInvoice(ctx, tx, org.ID, lineID, inv.ID, "bank_transfer"); err != nil {
		t.Fatalf("MatchInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	updated, err := core.GetInvoice(ctx, pool, org.ID, inv.ID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if updated.Status != "paid" {
		t.Fatalf("invoice status = %s, want paid", updated.Status)
	}
	if status, _ := bankLineStatus(t, pool, lineID); status != "matched" {
		t.Fatalf("bank line status = %s, want matched", status)
	}

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := recon.Unmatch(ctx, tx2, org.ID, lineID); err != nil {
		t.Fatalf("Unmatch: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	reverted, err := core.GetInvoice(ctx, pool, org.ID, inv.ID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if reverted.Status != "open" {
		t.Fatalf("invoice status after unmatch = %s, want open", reverted.Status)
	}
	if n := countPayments(t, pool, org.ID, inv.ID); n != 0 {
		t.Fatalf("payments after unmatch = %d, want 0", n)
	}
	if status, reason := bankLineStatus(t, pool, lineID); status != "unmatched" || reason != nil {
		t.Fatalf("bank line after unmatch = (%s, %v), want (unmatched, nil)", status, reason)
	}
}

// TestMatchInvoiceConcurrent covers the lock: two goroutines racing to
// match the same line each open their own transaction and call
// MatchInvoice; the bank line's FOR UPDATE lock serializes them, so exactly
// one succeeds (the other sees the line already matched and gets
// ErrInvalidState), and exactly one payment is recorded.
func TestMatchInvoiceConcurrent(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	inv := testInvoice(t, pool, org.ID, contactID, 20000)
	lineID := testBankLineID(t, pool, org.ID, 20000, mustDate(t, "2026-11-02"), "Transfer")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			defer tx.Rollback(ctx)
			if _, err := recon.MatchInvoice(ctx, tx, org.ID, lineID, inv.ID, "bank_transfer"); err != nil {
				errs[i] = err
				return
			}
			errs[i] = tx.Commit(ctx)
		}(i)
	}
	wg.Wait()

	var succeeded, conflicted int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, core.ErrInvalidState):
			conflicted++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded = %d, conflicted = %d, want 1 and 1", succeeded, conflicted)
	}
	if n := countPayments(t, pool, org.ID, inv.ID); n != 1 {
		t.Fatalf("payments = %d, want 1", n)
	}
}

// TestMatchInvoiceRejectsDebit422 covers that a debit line can never be
// matched to an invoice.
func TestMatchInvoiceRejectsDebit422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	inv := testInvoice(t, pool, org.ID, contactID, 20000)
	lineID := testBankLineID(t, pool, org.ID, -20000, mustDate(t, "2026-11-02"), "Payment out")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	_, err = recon.MatchInvoice(ctx, tx, org.ID, lineID, inv.ID, "bank_transfer")
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("MatchInvoice on debit: err = %v, want ErrInvalidInput", err)
	}
}

// TestCreateExpenseFromLineTotalMustMatch covers that the created
// expense's total must exactly equal -line.amount: a short total is
// rejected, the matching total succeeds and links the expense.
func TestCreateExpenseFromLineTotalMustMatch(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
	lineID := testBankLineID(t, pool, org.ID, -16500, mustDate(t, "2026-11-03"), "Plumber")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	_, _, err = recon.CreateExpenseFromLine(ctx, tx, org.ID, lineID, recon.NewExpense{
		Description: "Plumber",
		Lines:       []recon.NewExpenseLine{{CategoryID: repairs.ID, Amount: 16400}},
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("short total: err = %v, want ErrInvalidInput", err)
	}
	tx.Rollback(ctx)

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	exp, _, err := recon.CreateExpenseFromLine(ctx, tx2, org.ID, lineID, recon.NewExpense{
		Description: "Plumber",
		Lines:       []recon.NewExpenseLine{{CategoryID: repairs.ID, Amount: 16500}},
	})
	if err != nil {
		t.Fatalf("CreateExpenseFromLine: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if exp.SpentOn.Format("2006-01-02") != "2026-11-03" {
		t.Errorf("spent_on = %s, want defaulted to posted_on 2026-11-03", exp.SpentOn.Format("2006-01-02"))
	}
	if exp.BankLineID == nil || *exp.BankLineID != lineID {
		t.Errorf("bank_line_id = %v, want %d", exp.BankLineID, lineID)
	}
	if status, _ := bankLineStatus(t, pool, lineID); status != "matched" {
		t.Fatalf("bank line status = %s, want matched", status)
	}
}

// TestSuggestRanksPaymentReference covers that, between two open invoices
// with the same outstanding amount, the one whose payment reference
// appears in the line's description scores 100 and sorts first.
func TestSuggestRanksPaymentReference(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	match := testInvoice(t, pool, org.ID, contactID, 30000)
	other := testInvoice(t, pool, org.ID, contactID, 30000)
	_ = other
	lineID := testBankLineID(t, pool, org.ID, 30000, mustDate(t, "2026-11-04"), "Transfer "+match.PaymentReference)

	suggestions, err := recon.Suggest(ctx, pool, org.ID, lineID)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(suggestions) < 2 {
		t.Fatalf("len(suggestions) = %d, want at least 2", len(suggestions))
	}
	if suggestions[0].Kind != "invoice" || suggestions[0].ID != match.ID || suggestions[0].Score != 100 {
		t.Fatalf("suggestions[0] = %+v, want invoice %d with score 100", suggestions[0], match.ID)
	}
	if suggestions[1].Score >= 100 {
		t.Fatalf("suggestions[1].Score = %d, want < 100", suggestions[1].Score)
	}
}

// TestAutoMatchOnlyUnambiguous covers that AutoMatch matches a clean,
// unambiguous credit line but skips one whose amount fits two invoices and
// whose description names neither's payment reference.
func TestAutoMatchOnlyUnambiguous(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)

	clean := testInvoice(t, pool, org.ID, contactID, 40000)
	testBankLineID(t, pool, org.ID, 40000, mustDate(t, "2026-11-05"), "Transfer "+clean.PaymentReference)

	testInvoice(t, pool, org.ID, contactID, 25000)
	testInvoice(t, pool, org.ID, contactID, 25000)
	testBankLineID(t, pool, org.ID, 25000, mustDate(t, "2026-11-05"), "Generic transfer, no reference")

	matched, err := recon.AutoMatch(ctx, pool, org.ID)
	if err != nil {
		t.Fatalf("AutoMatch: %v", err)
	}
	if matched != 1 {
		t.Fatalf("matched = %d, want 1", matched)
	}

	cleanUpdated, err := core.GetInvoice(ctx, pool, org.ID, clean.ID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if cleanUpdated.Status != "paid" {
		t.Fatalf("clean invoice status = %s, want paid", cleanUpdated.Status)
	}
}

// TestIgnoreReasonValidated covers that Ignore only accepts its five
// defined reasons and marks the line ignored with the given reason.
func TestIgnoreReasonValidated(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	lineID := testBankLineID(t, pool, org.ID, -5000, mustDate(t, "2026-11-06"), "Owner drawings")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := recon.Ignore(ctx, tx, org.ID, lineID, "not_a_reason"); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad reason: err = %v, want ErrInvalidInput", err)
	}
	tx.Rollback(ctx)

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := recon.Ignore(ctx, tx2, org.ID, lineID, "drawings"); err != nil {
		t.Fatalf("Ignore: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	status, reason := bankLineStatus(t, pool, lineID)
	if status != "ignored" || reason == nil || *reason != "drawings" {
		t.Fatalf("bank line = (%s, %v), want (ignored, drawings)", status, reason)
	}
}

// TestAutoMatchRouteAuditsActor covers that a route-triggered auto-match
// carries the requesting user as the match_invoice audit's actor, not
// NULL - AutoMatch runs as business logic shared with other, actor-less
// callers, so the HTTP handler is responsible for putting the actor on the
// context before calling it.
func TestAutoMatchRouteAuditsActor(t *testing.T) {
	pool, handler, token, orgID := reconTestSetup(t)
	ctx := context.Background()
	contactID := createContact(t, pool, orgID)
	inv := testInvoice(t, pool, orgID, contactID, 15000)
	testBankLineID(t, pool, orgID, 15000, mustDate(t, "2026-11-07"), "Transfer "+inv.PaymentReference)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/reconcile/auto", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var actorID *int64
	if err := pool.QueryRow(ctx, `
		SELECT actor_user_id FROM audit_log
		WHERE org_id = $1 AND entity = 'bank_line' AND action = 'match_invoice'
		ORDER BY id DESC LIMIT 1`,
		orgID,
	).Scan(&actorID); err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	if actorID == nil {
		t.Fatalf("audit_log actor_user_id = nil, want the requesting user's id")
	}
}

// TestUnmatchAuditRecordsDeletedPayment covers that Unmatch's audit diff
// names exactly what it deleted/reopened - not just that something
// changed - since it's the one action in this package that hard-deletes
// money-moving rows.
func TestUnmatchAuditRecordsDeletedPayment(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	contactID := createContact(t, pool, org.ID)
	inv := testInvoice(t, pool, org.ID, contactID, 12000)
	lineID := testBankLineID(t, pool, org.ID, 12000, mustDate(t, "2026-11-08"), "Transfer")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	payment, err := recon.MatchInvoice(ctx, tx, org.ID, lineID, inv.ID, "bank_transfer")
	if err != nil {
		t.Fatalf("MatchInvoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := recon.Unmatch(ctx, tx2, org.ID, lineID); err != nil {
		t.Fatalf("Unmatch: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var diff []byte
	if err := pool.QueryRow(ctx, `
		SELECT diff FROM audit_log
		WHERE org_id = $1 AND entity = 'bank_line' AND entity_id = $2 AND action = 'unmatch'
		ORDER BY id DESC LIMIT 1`,
		org.ID, lineID,
	).Scan(&diff); err != nil {
		t.Fatalf("reading audit_log diff: %v", err)
	}

	var parsed struct {
		DeletedPayments []struct {
			ID        int64 `json:"id"`
			InvoiceID int64 `json:"invoice_id"`
			Amount    int64 `json:"amount"`
		} `json:"deleted_payments"`
		ReopenedInvoiceIDs []int64 `json:"reopened_invoice_ids"`
	}
	if err := json.Unmarshal(diff, &parsed); err != nil {
		t.Fatalf("unmarshaling diff: %v", err)
	}
	if len(parsed.DeletedPayments) != 1 {
		t.Fatalf("deleted_payments = %v, want exactly 1", parsed.DeletedPayments)
	}
	if parsed.DeletedPayments[0].ID != payment.ID || parsed.DeletedPayments[0].Amount != 12000 || parsed.DeletedPayments[0].InvoiceID != inv.ID {
		t.Fatalf("deleted_payments[0] = %+v, want {id:%d, invoice_id:%d, amount:12000}", parsed.DeletedPayments[0], payment.ID, inv.ID)
	}
	if len(parsed.ReopenedInvoiceIDs) != 1 || parsed.ReopenedInvoiceIDs[0] != inv.ID {
		t.Fatalf("reopened_invoice_ids = %v, want [%d]", parsed.ReopenedInvoiceIDs, inv.ID)
	}
}

// TestMatchExpenseAlreadyLinked covers the full MatchExpense happy path
// (link, mark matched) and then that a second line can't be matched to the
// same already-linked expense (409).
func TestMatchExpenseAlreadyLinked(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")

	exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-09"),
		Description: "Plumber",
		Lines:       []recon.NewExpenseLine{{CategoryID: repairs.ID, Amount: 9900}},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}

	line1 := testBankLineID(t, pool, org.ID, -9900, mustDate(t, "2026-11-09"), "Plumber payment")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := recon.MatchExpense(ctx, tx, org.ID, line1, exp.ID); err != nil {
		t.Fatalf("MatchExpense: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if status, _ := bankLineStatus(t, pool, line1); status != "matched" {
		t.Fatalf("line1 status = %s, want matched", status)
	}

	line2 := testBankLineID(t, pool, org.ID, -9900, mustDate(t, "2026-11-09"), "Plumber payment duplicate")
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx2.Rollback(ctx)
	err = recon.MatchExpense(ctx, tx2, org.ID, line2, exp.ID)
	if !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("already-linked expense: err = %v, want ErrInvalidState", err)
	}
}
