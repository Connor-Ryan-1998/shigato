//go:build integration

package property_test

import (
	"strings"
	"sync"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

func TestRunRentCreatesOnePeriodAndInvoice(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID // residential

	asOf := testkit.Date("2026-11-05")
	e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

	// Default invoice_lead_days is 7.
	result, err := property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("property.RunRent: %v", err)
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	e.Settings(org, testkit.RentRun(7, false)) // 2 periods per run

	asOf := testkit.Date("2026-11-05")
	lease := e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

	var wg sync.WaitGroup
	results := make([]property.RunResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("property.RunRent[%d]: %v", i, err)
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
	if got := e.Count("invoices", "org_id = $1 AND source = 'rent'", org.ID); got != 2 {
		t.Fatalf("rent invoices = %d, want 2", got)
	}
}

func TestRunRentCatchUp(t *testing.T) {
	e := testkit.New(t)
	ctx := e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	e.Settings(org, testkit.RentRun(0, false))

	asOf := testkit.Date("2026-11-05")
	// start is 10 weeks before asOf; paidTo is the end of period 6 (the
	// 7th period, 0-indexed), which lands 3 weeks (and a day) before
	// asOf, leaving periods 7, 8, 9 and 10 (the last starting on asOf
	// itself) uninvoiced.
	start := asOf.AddDate(0, 0, -70)
	paidTo := asOf.AddDate(0, 0, -22)
	e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(start), testkit.PaidTo(paidTo))

	result, err := property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("property.RunRent: %v", err)
	}
	if result.Created != 4 {
		t.Fatalf("Created = %d, want 4", result.Created)
	}

	// A second run the same day must find nothing left to invoice.
	result2, err := property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("second property.RunRent: %v", err)
	}
	if result2.Created != 0 {
		t.Fatalf("second run Created = %d, want 0", result2.Created)
	}
}

func TestRunRentAutoSendFailsWithoutPayOptions(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	e.Settings(org, testkit.RentRun(0, true)) // no bank details set

	asOf := testkit.Date("2026-11-05")
	lease := e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

	result, err := property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("property.RunRent: %v", err)
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

	ledger, err := property.GetLedger(ctx, pool, org.ID, lease.ID, asOf)
	if err != nil {
		t.Fatalf("property.GetLedger: %v", err)
	}
	if ledger.Unissued <= 0 {
		t.Fatalf("Unissued = %d, want > 0 (the failed-to-issue draft's amount_due)", ledger.Unissued)
	}
}

// TestLedgerArrears covers Ruling 20/21: arrears is only what's past due,
// invoiced-but-not-yet-due is reported separately, and opening arrears is
// a real invoice whose outstanding balance (not the static figure) counts.
func TestLedgerArrears(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID // residential
	e.Settings(org, testkit.RentRun(7, false))

	asOf := testkit.Date("2026-11-05")
	tenantID := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID
	lease, err := property.CreateLease(ctx, pool, org.ID, property.NewLease{
		PropertyID: propertyID, StartDate: asOf, Frequency: property.Weekly, RentAmount: 50000,
		OpeningArrears: 10000,
		Tenants:        []property.NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("property.CreateLease: %v", err)
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

	if _, err := property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf); err != nil {
		t.Fatalf("property.RunRent: %v", err)
	}

	ledger, err := property.GetLedger(ctx, pool, org.ID, lease.ID, asOf)
	if err != nil {
		t.Fatalf("property.GetLedger: %v", err)
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
		e.Issue(org, id, asOf)
	}
	if _, _, err := core.RecordPayment(ctx, pool, org.ID, core.NewPayment{
		InvoiceID: *ledger.Periods[0].InvoiceID, Amount: 50000, PaidOn: asOf, Method: "bank_transfer",
	}); err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	// On its due date nothing is in arrears yet.
	ledger, err = property.GetLedger(ctx, pool, org.ID, lease.ID, asOf)
	if err != nil {
		t.Fatalf("property.GetLedger(due date): %v", err)
	}
	if ledger.Arrears != 0 || ledger.InvoicedNotDue != 60000 || ledger.Unissued != 0 {
		t.Fatalf("on due date: %+v, want Arrears 0, InvoicedNotDue 60000, Unissued 0", ledger)
	}

	// The day after: opening arrears is overdue, period 1 is not yet due.
	notYetDue := testkit.Date("2026-11-06")
	ledger, err = property.GetLedger(ctx, pool, org.ID, lease.ID, notYetDue)
	if err != nil {
		t.Fatalf("property.GetLedger(not yet due): %v", err)
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
	ledger, err = property.GetLedger(ctx, pool, org.ID, lease.ID, notYetDue)
	if err != nil {
		t.Fatalf("property.GetLedger(after part payment): %v", err)
	}
	if ledger.Arrears != 6000 || ledger.OpeningArrears != 6000 {
		t.Fatalf("after part payment: Arrears/OpeningArrears = %d/%d, want 6000/6000", ledger.Arrears, ledger.OpeningArrears)
	}

	// Once period 1's due date passes, it's in arrears too.
	ledger, err = property.GetLedger(ctx, pool, org.ID, lease.ID, testkit.Date("2026-11-13"))
	if err != nil {
		t.Fatalf("property.GetLedger(overdue): %v", err)
	}
	if ledger.Arrears != 56000 || ledger.InvoicedNotDue != 0 {
		t.Fatalf("overdue: Arrears/InvoicedNotDue = %d/%d, want 56000/0", ledger.Arrears, ledger.InvoicedNotDue)
	}
}

func TestCreateLeaseNoOpeningArrearsNoInvoice(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	lease := e.Lease(org, e.Property(org).ID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(testkit.Date("2026-11-05")))
	if lease.OpeningArrearsInvoiceID != nil {
		t.Fatalf("OpeningArrearsInvoiceID = %d, want nil with no opening arrears", *lease.OpeningArrearsInvoiceID)
	}
	if got := e.Count("invoices", "org_id = $1 AND source = 'rent'", org.ID); got != 0 {
		t.Fatalf("rent invoices = %d, want 0", got)
	}
}

func TestNonResidentialGST(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org, testkit.Commercial(), testkit.PropertyName("1 Commerce Rd")).ID
	e.Settings(org, testkit.GSTRegistered(true))
	e.Settings(org, testkit.RentRun(0, false))

	asOf := testkit.Date("2026-11-05")
	e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

	if _, err := property.RunRent(ctx, e.BillingService(), org.ID, "acme", asOf); err != nil {
		t.Fatalf("property.RunRent: %v", err)
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

func TestRunRentRebillsVoidedPeriod(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	e.Settings(org, testkit.RentRun(0, true)) // one period per run, issued
	e.Settings(org, testkit.PayOptions())

	asOf := testkit.Date("2026-11-05")
	lease := e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))
	b := e.BillingService()

	if _, err := property.RunRent(ctx, b, org.ID, "acme", asOf); err != nil {
		t.Fatalf("property.RunRent: %v", err)
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
	results := make([]property.RunResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = property.RunRent(ctx, b, org.ID, "acme", asOf)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("property.RunRent[%d]: %v", i, err)
		}
	}
	if got := results[0].Created + results[1].Created; got != 1 {
		t.Fatalf("total Created after void = %d, want 1", got)
	}
	if got := e.Count("invoices", "org_id = $1 AND source = 'rent' AND status <> 'void'", org.ID); got != 1 {
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
	again, err := property.RunRent(ctx, b, org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("third property.RunRent: %v", err)
	}
	if again.Created != 0 {
		t.Fatalf("third run Created = %d, want 0", again.Created)
	}
}

// TestRunRentRebillsVoidedDraftPeriod covers Ruling 28: core.VoidInvoice now
// accepts a 'draft' invoice (not just 'open'), and a voided *draft* rent
// invoice must stay reclaimable by property.RunRent, the same way a voided open one
// already is.
func TestRunRentRebillsVoidedDraftPeriod(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	propertyID := e.Property(org).ID
	e.Settings(org, testkit.RentRun(0, false)) // one period per run, left as a draft

	asOf := testkit.Date("2026-11-05")
	lease := e.Lease(org, propertyID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))
	b := e.BillingService()

	if _, err := property.RunRent(ctx, b, org.ID, "acme", asOf); err != nil {
		t.Fatalf("property.RunRent: %v", err)
	}
	var draftID int64
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT rp.invoice_id, i.status FROM rent_periods rp JOIN invoices i ON i.org_id = rp.org_id AND i.id = rp.invoice_id WHERE rp.lease_id = $1`,
		lease.ID,
	).Scan(&draftID, &status); err != nil {
		t.Fatalf("selecting period invoice: %v", err)
	}
	if status != "draft" {
		t.Fatalf("precondition: period invoice status = %q, want draft", status)
	}

	if _, err := b.Void(ctx, org.ID, "acme", draftID); err != nil {
		t.Fatalf("Void draft: %v", err)
	}

	again, err := property.RunRent(ctx, b, org.ID, "acme", asOf)
	if err != nil {
		t.Fatalf("property.RunRent after voiding draft: %v", err)
	}
	if again.Created != 1 {
		t.Fatalf("Created after voiding draft = %d, want 1", again.Created)
	}

	var periods int
	var newInvoiceID int64
	if err := pool.QueryRow(ctx, "SELECT count(*), max(invoice_id) FROM rent_periods WHERE lease_id = $1", lease.ID).Scan(&periods, &newInvoiceID); err != nil {
		t.Fatalf("selecting periods: %v", err)
	}
	if periods != 1 || newInvoiceID == draftID {
		t.Fatalf("periods = %d, invoice_id = %d (old %d); want 1 period relinked to a new invoice", periods, newInvoiceID, draftID)
	}
}
