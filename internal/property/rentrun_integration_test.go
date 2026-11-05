//go:build integration

package property

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/payments"
)

// noProvider reports every org has no payment provider configured, so
// billing.Service.Issue's best-effort send is a no-op.
func noProvider(string) (payments.Provider, error) {
	return nil, payments.ErrNoProvider
}

// testService builds a billing.Service for RunRent tests: no payment
// provider, UTC.
func testService(pool *pgxpool.Pool) *billing.Service {
	return &billing.Service{Pool: pool, GetEnv: func(string) string { return "" }, Payments: noProvider, Location: time.UTC}
}

// testCommercialProperty creates a non-residential property.
func testCommercialProperty(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	p, err := CreateProperty(context.Background(), pool, orgID, Property{Name: "1 Commerce Rd", Residential: false})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

// setOrgLeadAndAutoSend sets an org's invoice_lead_days and auto_send_rent
// directly, bypassing the OrgSettings validation this test has no need for.
func setOrgLeadAndAutoSend(t *testing.T, pool *pgxpool.Pool, orgID int64, leadDays int, autoSend bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"UPDATE orgs SET invoice_lead_days = $1, auto_send_rent = $2 WHERE id = $3",
		leadDays, autoSend, orgID,
	); err != nil {
		t.Fatalf("setting org invoice_lead_days/auto_send_rent: %v", err)
	}
}

// setOrgGSTRegistered sets an org's gst_registered flag directly.
func setOrgGSTRegistered(t *testing.T, pool *pgxpool.Pool, orgID int64, registered bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "UPDATE orgs SET gst_registered = $1 WHERE id = $2", registered, orgID); err != nil {
		t.Fatalf("setting org gst_registered: %v", err)
	}
}

// setOrgPayOptions gives an org two payment options (bank transfer and
// PayID), satisfying billing.Service.Issue's QLD pay-options rule.
func setOrgPayOptions(t *testing.T, pool *pgxpool.Pool, orgID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"UPDATE orgs SET bank_bsb = $1, bank_account = $2, payid = $3 WHERE id = $4",
		"062-000", "12345678", "landlord@example.com", orgID,
	); err != nil {
		t.Fatalf("setting org pay options: %v", err)
	}
}

// testWeeklyLease creates a weekly lease of rentAmount on propertyID,
// starting on start, with one primary tenant.
func testWeeklyLease(t *testing.T, pool *pgxpool.Pool, orgID, propertyID int64, start time.Time, rentAmount core.Cents, paidTo *time.Time) Lease {
	t.Helper()
	tenantID := testContact(t, pool, orgID, "tenant", "Test Tenant")
	lease, err := CreateLease(context.Background(), pool, orgID, NewLease{
		PropertyID: propertyID, StartDate: start, Frequency: Weekly, RentAmount: rentAmount,
		PaidToDate: paidTo,
		Tenants:    []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return lease
}

// rentInvoiceCount counts invoices with source 'rent' for orgID.
func rentInvoiceCount(t *testing.T, pool *pgxpool.Pool, orgID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM invoices WHERE org_id = $1 AND source = 'rent'", orgID,
	).Scan(&n); err != nil {
		t.Fatalf("counting rent invoices: %v", err)
	}
	return n
}

func TestRunRentCreatesOnePeriodAndInvoice(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID) // residential

	asOf := mustDate(t, "2026-11-05")
	testWeeklyLease(t, pool, org.ID, propertyID, asOf, 50000, nil)

	// Default invoice_lead_days is 7.
	result, err := RunRent(ctx, testService(pool), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("RunRent: %v", err)
	}
	if result.Created != 2 {
		t.Fatalf("Created = %d, want 2", result.Created)
	}
	if result.Issued != 0 || result.Failed != 0 {
		t.Fatalf("Issued/Failed = %d/%d, want 0/0 (auto_send_rent off)", result.Issued, result.Failed)
	}

	rows, err := pool.Query(ctx, `
		SELECT l.gst_code FROM invoice_lines l
		JOIN invoices i ON i.id = l.invoice_id
		WHERE i.org_id = $1 AND i.source = 'rent' AND l.description LIKE 'Rent %'`, org.ID)
	if err != nil {
		t.Fatalf("querying lines: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if code != "INP" {
			t.Fatalf("rent line gst_code = %s, want INP for a residential property", code)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("rent lines = %d, want 2", n)
	}
}

func TestRunRentIdempotentConcurrent(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	setOrgLeadAndAutoSend(t, pool, org.ID, 7, false) // 2 periods per run

	asOf := mustDate(t, "2026-11-05")
	lease := testWeeklyLease(t, pool, org.ID, propertyID, asOf, 50000, nil)

	var wg sync.WaitGroup
	results := make([]RunResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = RunRent(ctx, testService(pool), org.ID, "acme", asOf)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("RunRent[%d]: %v", i, err)
		}
	}
	if results[0].Created+results[1].Created != 2 {
		t.Fatalf("total Created = %d, want 2 (two periods, each claimed exactly once)", results[0].Created+results[1].Created)
	}

	var periodCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rent_periods WHERE lease_id = $1", lease.ID).Scan(&periodCount); err != nil {
		t.Fatalf("counting rent_periods: %v", err)
	}
	if periodCount != 2 {
		t.Fatalf("rent_periods rows = %d, want 2", periodCount)
	}
	if got := rentInvoiceCount(t, pool, org.ID); got != 2 {
		t.Fatalf("rent invoices = %d, want 2", got)
	}
}

func TestRunRentCatchUp(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	setOrgLeadAndAutoSend(t, pool, org.ID, 0, false)

	asOf := mustDate(t, "2026-11-05")
	// start is 10 weeks before asOf; paidTo is the end of period 6 (the
	// 7th period, 0-indexed), which lands 3 weeks (and a day) before
	// asOf, leaving periods 7, 8, 9 and 10 (the last starting on asOf
	// itself) uninvoiced.
	start := asOf.AddDate(0, 0, -70)
	paidTo := asOf.AddDate(0, 0, -22)
	testWeeklyLease(t, pool, org.ID, propertyID, start, 50000, &paidTo)

	result, err := RunRent(ctx, testService(pool), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("RunRent: %v", err)
	}
	if result.Created != 4 {
		t.Fatalf("Created = %d, want 4", result.Created)
	}

	// A second run the same day must find nothing left to invoice.
	result2, err := RunRent(ctx, testService(pool), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("second RunRent: %v", err)
	}
	if result2.Created != 0 {
		t.Fatalf("second run Created = %d, want 0", result2.Created)
	}
}

func TestRunRentAutoSendFailsWithoutPayOptions(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	setOrgLeadAndAutoSend(t, pool, org.ID, 0, true) // no bank details set

	asOf := mustDate(t, "2026-11-05")
	lease := testWeeklyLease(t, pool, org.ID, propertyID, asOf, 50000, nil)

	result, err := RunRent(ctx, testService(pool), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("RunRent: %v", err)
	}
	if result.Created != 1 || result.Failed != 1 || result.Issued != 0 {
		t.Fatalf("result = %+v, want Created=1 Failed=1 Issued=0", result)
	}

	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM invoices WHERE org_id = $1 AND source = 'rent'", org.ID).Scan(&status); err != nil {
		t.Fatalf("selecting invoice status: %v", err)
	}
	if status != "draft" {
		t.Fatalf("status = %s, want draft (failed issue must not change it)", status)
	}

	var n int
	var diff []byte
	if err := pool.QueryRow(ctx,
		"SELECT count(*), max(diff::text) FROM audit_log WHERE org_id = $1 AND entity = 'invoice' AND action = 'rent_issue_failed'", org.ID,
	).Scan(&n, &diff); err != nil {
		t.Fatalf("counting audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("rent_issue_failed audit rows = %d, want 1", n)
	}
	if !strings.Contains(string(diff), "too_few_pay_options") {
		t.Fatalf("audit diff = %s, want it to contain too_few_pay_options", diff)
	}

	ledger, err := GetLedger(ctx, pool, org.ID, lease.ID, asOf)
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if ledger.Unissued <= 0 {
		t.Fatalf("Unissued = %d, want > 0 (the failed-to-issue draft's amount_due)", ledger.Unissued)
	}
}

// issueAt issues draft invoiceID with an explicit issue date, so a test's
// due dates don't depend on the wall clock.
func issueAt(t *testing.T, pool *pgxpool.Pool, orgID, invoiceID int64, on time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := core.IssueInvoice(ctx, tx, orgID, invoiceID, on); err != nil {
		t.Fatalf("IssueInvoice(%d): %v", invoiceID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestLedgerArrears covers Ruling 20/21: arrears is only what's past due,
// invoiced-but-not-yet-due is reported separately, and opening arrears is
// a real invoice whose outstanding balance (not the static figure) counts.
func TestLedgerArrears(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID) // residential
	setOrgLeadAndAutoSend(t, pool, org.ID, 7, false)

	asOf := mustDate(t, "2026-11-05")
	tenantID := testContact(t, pool, org.ID, "tenant", "Test Tenant")
	lease, err := CreateLease(ctx, pool, org.ID, NewLease{
		PropertyID: propertyID, StartDate: asOf, Frequency: Weekly, RentAmount: 50000,
		OpeningArrears: 10000,
		Tenants:        []NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.OpeningArrearsInvoiceID == nil {
		t.Fatalf("OpeningArrearsInvoiceID = nil, want a draft invoice")
	}
	opening, err := core.GetInvoice(ctx, pool, org.ID, *lease.OpeningArrearsInvoiceID)
	if err != nil {
		t.Fatalf("GetInvoice(opening): %v", err)
	}
	if opening.Status != "draft" || opening.Source != "rent" || opening.ContactID != tenantID ||
		opening.DueOn == nil || !opening.DueOn.Equal(asOf) || len(opening.Lines) != 1 ||
		opening.Lines[0].Description != "Opening arrears" || opening.Lines[0].GSTCode != core.INP || opening.Total != 10000 {
		t.Fatalf("opening arrears invoice = %+v, want a 10000 draft rent invoice to the tenant, due on start, one INP line", opening)
	}
	var linePropertyID *int64
	if err := pool.QueryRow(ctx, "SELECT property_id FROM invoice_lines WHERE org_id = $1 AND invoice_id = $2", org.ID, opening.ID).Scan(&linePropertyID); err != nil {
		t.Fatalf("selecting line property_id: %v", err)
	}
	if linePropertyID == nil || *linePropertyID != propertyID {
		t.Fatalf("line property_id = %v, want %d", linePropertyID, propertyID)
	}

	if _, err := RunRent(ctx, testService(pool), org.ID, "acme", asOf); err != nil {
		t.Fatalf("RunRent: %v", err)
	}

	ledger, err := GetLedger(ctx, pool, org.ID, lease.ID, asOf)
	if err != nil {
		t.Fatalf("GetLedger: %v", err)
	}
	if len(ledger.Periods) != 2 || ledger.Periods[0].InvoiceID == nil || ledger.Periods[1].InvoiceID == nil {
		t.Fatalf("Periods = %+v, want 2 periods each with an invoice", ledger.Periods)
	}
	if ledger.Arrears != 0 || ledger.Unissued != 110000 || ledger.OpeningArrears != 10000 {
		t.Fatalf("all-draft ledger = %+v, want Arrears 0, Unissued 110000, OpeningArrears 10000", ledger)
	}

	// Issue everything on asOf: period 0 and opening arrears due 11-05,
	// period 1 due 11-12.
	for _, id := range []int64{*ledger.Periods[0].InvoiceID, *ledger.Periods[1].InvoiceID, opening.ID} {
		issueAt(t, pool, org.ID, id, asOf)
	}
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: *ledger.Periods[0].InvoiceID, Amount: 50000, PaidOn: asOf, Method: "bank_transfer",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	// On its due date nothing is in arrears yet.
	ledger, err = GetLedger(ctx, pool, org.ID, lease.ID, asOf)
	if err != nil {
		t.Fatalf("GetLedger(due date): %v", err)
	}
	if ledger.Arrears != 0 || ledger.InvoicedNotDue != 60000 || ledger.Unissued != 0 {
		t.Fatalf("on due date: %+v, want Arrears 0, InvoicedNotDue 60000, Unissued 0", ledger)
	}

	// The day after: opening arrears is overdue, period 1 is not yet due.
	notYetDue := mustDate(t, "2026-11-06")
	ledger, err = GetLedger(ctx, pool, org.ID, lease.ID, notYetDue)
	if err != nil {
		t.Fatalf("GetLedger(not yet due): %v", err)
	}
	if ledger.Arrears != 10000 || ledger.InvoicedNotDue != 50000 {
		t.Fatalf("not yet due: Arrears/InvoicedNotDue = %d/%d, want 10000/50000", ledger.Arrears, ledger.InvoicedNotDue)
	}

	// Paying part of the opening arrears reduces arrears.
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: opening.ID, Amount: 4000, PaidOn: notYetDue, Method: "bank_transfer",
	}); err != nil {
		t.Fatalf("RecordPayment(opening): %v", err)
	}
	ledger, err = GetLedger(ctx, pool, org.ID, lease.ID, notYetDue)
	if err != nil {
		t.Fatalf("GetLedger(after part payment): %v", err)
	}
	if ledger.Arrears != 6000 || ledger.OpeningArrears != 6000 {
		t.Fatalf("after part payment: Arrears/OpeningArrears = %d/%d, want 6000/6000", ledger.Arrears, ledger.OpeningArrears)
	}

	// Once period 1's due date passes, it's in arrears too.
	ledger, err = GetLedger(ctx, pool, org.ID, lease.ID, mustDate(t, "2026-11-13"))
	if err != nil {
		t.Fatalf("GetLedger(overdue): %v", err)
	}
	if ledger.Arrears != 56000 || ledger.InvoicedNotDue != 0 {
		t.Fatalf("overdue: Arrears/InvoicedNotDue = %d/%d, want 56000/0", ledger.Arrears, ledger.InvoicedNotDue)
	}
}

func TestCreateLeaseNoOpeningArrearsNoInvoice(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")
	lease := testWeeklyLease(t, pool, org.ID, testProperty(t, pool, org.ID), mustDate(t, "2026-11-05"), 50000, nil)
	if lease.OpeningArrearsInvoiceID != nil {
		t.Fatalf("OpeningArrearsInvoiceID = %d, want nil with no opening arrears", *lease.OpeningArrearsInvoiceID)
	}
	if got := rentInvoiceCount(t, pool, org.ID); got != 0 {
		t.Fatalf("rent invoices = %d, want 0", got)
	}
}

func TestNonResidentialGST(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testCommercialProperty(t, pool, org.ID)
	setOrgGSTRegistered(t, pool, org.ID, true)
	setOrgLeadAndAutoSend(t, pool, org.ID, 0, false)

	asOf := mustDate(t, "2026-11-05")
	testWeeklyLease(t, pool, org.ID, propertyID, asOf, 50000, nil)

	if _, err := RunRent(ctx, testService(pool), org.ID, "acme", asOf); err != nil {
		t.Fatalf("RunRent: %v", err)
	}

	var invID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM invoices WHERE org_id = $1 AND source = 'rent'", org.ID).Scan(&invID); err != nil {
		t.Fatalf("selecting rent invoice: %v", err)
	}
	inv, err := core.GetInvoice(ctx, pool, org.ID, invID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if len(inv.Lines) != 1 || inv.Lines[0].GSTCode != core.GST {
		t.Fatalf("Lines = %+v, want one GST-coded line", inv.Lines)
	}
	if inv.Lines[0].GSTAmount != 5000 {
		t.Fatalf("GSTAmount = %d, want 5000 (10%% of 50000)", inv.Lines[0].GSTAmount)
	}
}

// liveRentInvoices counts orgID's rent invoices that aren't void.
func liveRentInvoices(t *testing.T, pool *pgxpool.Pool, orgID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM invoices WHERE org_id = $1 AND source = 'rent' AND status <> 'void'", orgID,
	).Scan(&n); err != nil {
		t.Fatalf("counting live rent invoices: %v", err)
	}
	return n
}

func TestRunRentRebillsVoidedPeriod(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	propertyID := testProperty(t, pool, org.ID)
	setOrgLeadAndAutoSend(t, pool, org.ID, 0, true) // one period per run, issued
	setOrgPayOptions(t, pool, org.ID)

	asOf := mustDate(t, "2026-11-05")
	lease := testWeeklyLease(t, pool, org.ID, propertyID, asOf, 50000, nil)
	b := testService(pool)

	if _, err := RunRent(ctx, b, org.ID, "acme", asOf); err != nil {
		t.Fatalf("RunRent: %v", err)
	}
	var oldInvoiceID int64
	if err := pool.QueryRow(ctx, "SELECT invoice_id FROM rent_periods WHERE lease_id = $1", lease.ID).Scan(&oldInvoiceID); err != nil {
		t.Fatalf("selecting period invoice: %v", err)
	}
	if _, err := b.Void(ctx, org.ID, "acme", oldInvoiceID); err != nil {
		t.Fatalf("Void: %v", err)
	}

	// Two concurrent runs after the void must re-bill the period exactly once.
	var wg sync.WaitGroup
	results := make([]RunResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = RunRent(ctx, b, org.ID, "acme", asOf)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("RunRent[%d]: %v", i, err)
		}
	}
	if got := results[0].Created + results[1].Created; got != 1 {
		t.Fatalf("total Created after void = %d, want 1", got)
	}
	if got := liveRentInvoices(t, pool, org.ID); got != 1 {
		t.Fatalf("live rent invoices = %d, want 1", got)
	}

	var periods int
	var newInvoiceID int64
	if err := pool.QueryRow(ctx, "SELECT count(*), max(invoice_id) FROM rent_periods WHERE lease_id = $1", lease.ID).Scan(&periods, &newInvoiceID); err != nil {
		t.Fatalf("selecting periods: %v", err)
	}
	if periods != 1 || newInvoiceID == oldInvoiceID {
		t.Fatalf("periods = %d, invoice_id = %d (old %d); want 1 period relinked to a new invoice", periods, newInvoiceID, oldInvoiceID)
	}

	// And nothing more on a further run.
	again, err := RunRent(ctx, b, org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("third RunRent: %v", err)
	}
	if again.Created != 0 {
		t.Fatalf("third run Created = %d, want 0", again.Created)
	}
}
