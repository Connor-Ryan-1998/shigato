//go:build integration

package timesheets_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/payments"
	"shigoto/internal/timesheets"
)

// noBillingProvider reports every org has no payment provider configured,
// so billing.Service.Issue's best-effort send is a no-op.
func noBillingProvider(string) (payments.Provider, error) {
	return nil, payments.ErrNoProvider
}

// testBillingService builds a billing.Service for RunBilling tests: no
// payment provider, UTC.
func testBillingService(pool *pgxpool.Pool) *billing.Service {
	return &billing.Service{Pool: pool, GetEnv: func(string) string { return "" }, Payments: noBillingProvider, Location: time.UTC}
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
		"062-000", "12345678", "owner@example.com", orgID,
	); err != nil {
		t.Fatalf("setting org pay options: %v", err)
	}
}

// testEntry logs a time entry against taskID on workDate, under userID.
func testEntry(t *testing.T, pool *pgxpool.Pool, orgID, userID, taskID int64, workDate time.Time, minutes int) timesheets.Entry {
	t.Helper()
	e, err := timesheets.CreateEntry(context.Background(), pool, orgID, userID, timesheets.Entry{
		TaskID: taskID, WorkDate: workDate, Minutes: minutes,
	}, workDate)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	return e
}

func TestRunBillingGroupsByTask(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	setOrgGSTRegistered(t, pool, org.ID, true)
	userID := testUser(t, pool, "entries-groups@example.com")

	clientA := testContact(t, pool, org.ID, "client", "Client A")
	clientB := testContact(t, pool, org.ID, "client", "Client B")

	p1, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: clientA, Name: "P1", HourlyRate: 12000, GSTCode: core.GST,
	})
	if err != nil {
		t.Fatalf("CreateProject(P1): %v", err)
	}
	p2, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: clientA, Name: "P2", HourlyRate: 9500, GSTCode: core.GST,
	})
	if err != nil {
		t.Fatalf("CreateProject(P2): %v", err)
	}
	t1, err := timesheets.CreateTask(ctx, pool, org.ID, p1.ID, timesheets.Task{Name: "T1"})
	if err != nil {
		t.Fatalf("CreateTask(T1): %v", err)
	}
	t2, err := timesheets.CreateTask(ctx, pool, org.ID, p2.ID, timesheets.Task{Name: "T2"})
	if err != nil {
		t.Fatalf("CreateTask(T2): %v", err)
	}
	pB, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: clientB, Name: "PB", HourlyRate: 10000, GSTCode: core.GST,
	})
	if err != nil {
		t.Fatalf("CreateProject(PB): %v", err)
	}
	tB, err := timesheets.CreateTask(ctx, pool, org.ID, pB.ID, timesheets.Task{Name: "TB"})
	if err != nil {
		t.Fatalf("CreateTask(TB): %v", err)
	}

	nov15 := mustDate(t, "2026-11-15")
	testEntry(t, pool, org.ID, userID, t1.ID, nov15, 30)
	testEntry(t, pool, org.ID, userID, t1.ID, nov15, 20)
	testEntry(t, pool, org.ID, userID, t2.ID, nov15, 60)
	bEntry := testEntry(t, pool, org.ID, userID, tB.ID, nov15, 45)

	b := testBillingService(pool)
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: clientA, From: mustDate(t, "2026-11-01"), To: mustDate(t, "2026-11-30"), DueInDays: 14,
	})
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if result.EntryCount != 3 {
		t.Fatalf("EntryCount = %d, want 3", result.EntryCount)
	}
	if result.TotalMinutes != 110 {
		t.Fatalf("TotalMinutes = %d, want 110", result.TotalMinutes)
	}
	if len(result.Invoice.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2", len(result.Invoice.Lines))
	}
	if result.Invoice.Lines[0].UnitAmount != 10000 {
		t.Fatalf("Lines[0].UnitAmount = %d, want 10000", result.Invoice.Lines[0].UnitAmount)
	}
	if result.Invoice.Lines[1].UnitAmount != 9500 {
		t.Fatalf("Lines[1].UnitAmount = %d, want 9500", result.Invoice.Lines[1].UnitAmount)
	}
	if result.Invoice.Total != 21450 {
		t.Fatalf("Total = %d, want 21450", result.Invoice.Total)
	}

	bEntryAfter, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &tB.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(bEntryAfter) != 1 || bEntryAfter[0].InvoiceLineID != nil {
		t.Fatalf("client B's entry (id %d) got billed, want untouched", bEntry.ID)
	}
}

func TestRunBillingRangeInclusive(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	userID := testUser(t, pool, "entries-range@example.com")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	from := mustDate(t, "2026-11-01")
	to := mustDate(t, "2026-11-30")
	testEntry(t, pool, org.ID, userID, task.ID, from.AddDate(0, 0, -1), 10) // before range
	testEntry(t, pool, org.ID, userID, task.ID, from, 20)                   // on from
	testEntry(t, pool, org.ID, userID, task.ID, to, 30)                     // on to
	testEntry(t, pool, org.ID, userID, task.ID, to.AddDate(0, 0, 1), 40)    // after range

	b := testBillingService(pool)
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14,
	})
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if result.EntryCount != 2 {
		t.Fatalf("EntryCount = %d, want 2", result.EntryCount)
	}
	if result.TotalMinutes != 50 {
		t.Fatalf("TotalMinutes = %d, want 50", result.TotalMinutes)
	}
}

func TestRunBillingConcurrent(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	userID := testUser(t, pool, "entries-concurrent@example.com")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	from := mustDate(t, "2026-11-01")
	to := mustDate(t, "2026-11-30")
	testEntry(t, pool, org.ID, userID, task.ID, from, 30)
	testEntry(t, pool, org.ID, userID, task.ID, from.AddDate(0, 0, 1), 20)

	b := testBillingService(pool)
	in := timesheets.RunInput{ClientContactID: client, From: from, To: to, DueInDays: 14}

	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	for i := range results {
		go func(i int) {
			defer wg.Done()
			_, err := timesheets.RunBilling(ctx, b, org.ID, "acme", in)
			results[i] = err
		}(i)
	}
	wg.Wait()

	successes, invalidInputs := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, core.ErrInvalidInput):
			invalidInputs++
		default:
			t.Fatalf("RunBilling concurrent: unexpected error %v", err)
		}
	}
	if successes != 1 || invalidInputs != 1 {
		t.Fatalf("successes=%d invalidInputs=%d, want 1 and 1", successes, invalidInputs)
	}

	entries, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, e := range entries {
		if e.InvoiceLineID == nil {
			t.Fatalf("entry %d not billed after concurrent run", e.ID)
		}
	}

	// A new entry billed by a second run only picks up the new entry.
	newEntry := testEntry(t, pool, org.ID, userID, task.ID, from, 15)
	result2, err := timesheets.RunBilling(ctx, b, org.ID, "acme", in)
	if err != nil {
		t.Fatalf("second RunBilling: %v", err)
	}
	if result2.EntryCount != 1 || result2.TotalMinutes != 15 {
		t.Fatalf("second RunBilling = %+v, want EntryCount 1, TotalMinutes 15", result2)
	}
	after, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, e := range after {
		if e.ID == newEntry.ID && e.InvoiceLineID == nil {
			t.Fatalf("new entry %d not billed by second run", e.ID)
		}
	}
}

func TestRunBillingNothingUnbilled422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")

	b := testBillingService(pool)
	_, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: mustDate(t, "2026-11-01"), To: mustDate(t, "2026-11-30"), DueInDays: 14,
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("RunBilling with nothing unbilled: err = %v, want ErrInvalidInput", err)
	}
}

func TestVoidUnlocksEntries(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	setOrgPayOptions(t, pool, org.ID)
	userID := testUser(t, pool, "entries-void@example.com")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	from := mustDate(t, "2026-11-01")
	to := mustDate(t, "2026-11-30")
	entry := testEntry(t, pool, org.ID, userID, task.ID, from, 60)

	b := testBillingService(pool)
	// Void only accepts an 'open' invoice, so issue it first.
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14, Issue: true,
	})
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}

	if _, err := b.Void(ctx, org.ID, "acme", result.Invoice.ID); err != nil {
		t.Fatalf("Void: %v", err)
	}

	entries, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].InvoiceLineID != nil {
		t.Fatalf("entry %d still locked after void: %+v", entry.ID, entries)
	}

	// The entry can now be updated (not locked) ...
	actor := auth.Principal{UserID: entries[0].UserID, OrgID: org.ID, Role: auth.Owner}
	if _, err := timesheets.UpdateEntry(ctx, pool, org.ID, actor, entry.ID, timesheets.Entry{
		TaskID: task.ID, WorkDate: from, Minutes: 90, Note: "revised",
	}, to.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("UpdateEntry after void: %v", err)
	}

	// ... and a new run bills it again.
	result2, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14,
	})
	if err != nil {
		t.Fatalf("second RunBilling: %v", err)
	}
	if result2.EntryCount != 1 {
		t.Fatalf("second RunBilling EntryCount = %d, want 1", result2.EntryCount)
	}
}

func TestRunBillingIssueFlag(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	setOrgPayOptions(t, pool, org.ID)
	userID := testUser(t, pool, "entries-issue@example.com")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	from := mustDate(t, "2026-11-01")
	to := mustDate(t, "2026-11-30")
	testEntry(t, pool, org.ID, userID, task.ID, from, 60)

	b := testBillingService(pool)
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14, Issue: true,
	})
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if result.Invoice.Status != "open" {
		t.Fatalf("Status = %q, want open", result.Invoice.Status)
	}
	if result.Invoice.Number == nil {
		t.Fatalf("Number = nil, want assigned")
	}
}
