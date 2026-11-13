//go:build integration

package timesheets_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/testkit"
	"shigoto/internal/timesheets"
)

// TestRunBillingSkipsFixedProjects covers fixed-price projects: their time
// is tracked but never billed by a billing run.
func TestRunBillingSkipsFixedProjects(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	userID := e.User("fixed@example.com").ID
	client := e.Contact(org, testkit.Name("Client A")).ID
	b := e.BillingService()

	fixed := e.Project(org, client, testkit.ProjectName("Fixed"), testkit.ProjectRate(12000), testkit.FixedPrice())
	tm := e.Project(org, client, testkit.ProjectName("Hourly"))
	fixedTask := e.Task(org, fixed.ID, testkit.TaskName("Build"))
	tmTask := e.Task(org, tm.ID, testkit.TaskName("Support"))
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	fixedEntry := e.Entry(org, userID, timesheets.Entry{TaskID: fixedTask.ID, WorkDate: day, Minutes: 120}, day)
	in := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14}

	// Only fixed time: nothing to bill.
	if _, err := timesheets.RunBilling(ctx, b, org.ID, "acme", in); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("fixed-only run: err = %v, want ErrInvalidInput (no unbilled time)", err)
	}

	e.Entry(org, userID, timesheets.Entry{TaskID: tmTask.ID, WorkDate: day, Minutes: 60}, day)
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", in)
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if result.EntryCount != 1 || result.TotalMinutes != 60 {
		t.Fatalf("billed %d entries / %d minutes, want 1 / 60", result.EntryCount, result.TotalMinutes)
	}
	after, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &fixedTask.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(after) != 1 || after[0].ID != fixedEntry.ID || after[0].InvoiceLineID != nil {
		t.Fatalf("fixed entry after run = %+v, want still unbilled", after)
	}
}

func TestRunBillingGroupsByTask(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	userID := e.User("entries-groups@example.com").ID

	clientA := e.Contact(org, testkit.Name("Client A")).ID
	clientB := e.Contact(org, testkit.Name("Client B")).ID

	p1 := e.Project(org, clientA, testkit.ProjectName("P1"), testkit.ProjectRate(12000), testkit.ProjectGST("GST"))
	p2 := e.Project(org, clientA, testkit.ProjectName("P2"), testkit.ProjectRate(9500), testkit.ProjectGST("GST"))
	t1 := e.Task(org, p1.ID, testkit.TaskName("T1"))
	t2 := e.Task(org, p2.ID, testkit.TaskName("T2"))
	pB := e.Project(org, clientB, testkit.ProjectName("PB"), testkit.ProjectRate(10000), testkit.ProjectGST("GST"))
	tB := e.Task(org, pB.ID, testkit.TaskName("TB"))

	nov15 := testkit.Date("2026-11-15")
	e.Entry(org, userID, timesheets.Entry{TaskID: t1.ID, WorkDate: nov15, Minutes: 30}, nov15)
	e.Entry(org, userID, timesheets.Entry{TaskID: t1.ID, WorkDate: nov15, Minutes: 20}, nov15)
	e.Entry(org, userID, timesheets.Entry{TaskID: t2.ID, WorkDate: nov15, Minutes: 60}, nov15)
	bEntry := e.Entry(org, userID, timesheets.Entry{TaskID: tB.ID, WorkDate: nov15, Minutes: 45}, nov15)

	b := e.BillingService()
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: clientA, From: testkit.Date("2026-11-01"), To: testkit.Date("2026-11-30"), DueInDays: 14,
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
	e := testkit.New(t)
	ctx := e.Ctx
	org := e.Org("acme")
	userID := e.User("entries-range@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from.AddDate(0, 0, -1), Minutes: 10}, from.AddDate(0, 0, -1)) // before range
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 20}, from)                                     // on from
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: to, Minutes: 30}, to)                                         // on to
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: to.AddDate(0, 0, 1), Minutes: 40}, to.AddDate(0, 0, 1))       // after range

	b := e.BillingService()
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	userID := e.User("entries-concurrent@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 30}, from)
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from.AddDate(0, 0, 1), Minutes: 20}, from.AddDate(0, 0, 1))

	b := e.BillingService()
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
	newEntry := e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 15}, from)
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
	e := testkit.New(t)
	ctx := e.Ctx
	org := e.Org("acme")
	client := e.Contact(org, testkit.Name("A Client")).ID

	b := e.BillingService()
	_, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: testkit.Date("2026-11-01"), To: testkit.Date("2026-11-30"), DueInDays: 14,
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("RunBilling with nothing unbilled: err = %v, want ErrInvalidInput", err)
	}
}

func TestVoidUnlocksEntries(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.PayOptions())
	userID := e.User("entries-void@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	entry := e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 60}, from)

	b := e.BillingService()
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

// TestVoidUnlocksEntriesFromDraft covers Ruling 28: voiding a billing-run
// draft (never issued, so core.VoidInvoice must accept 'draft' as well as
// 'open') unlocks its time entries the same way voiding an open invoice
// does, and a later run bills them again.
func TestVoidUnlocksEntriesFromDraft(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	userID := e.User("entries-void-draft@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 60}, from)

	b := e.BillingService()
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14,
	})
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if result.Invoice.Status != "draft" {
		t.Fatalf("Invoice.Status = %q, want draft", result.Invoice.Status)
	}

	if _, err := b.Void(ctx, org.ID, "acme", result.Invoice.ID); err != nil {
		t.Fatalf("Void draft: %v", err)
	}

	voided, err := core.GetInvoice(ctx, pool, org.ID, result.Invoice.ID)
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if voided.Status != "void" {
		t.Fatalf("Status after void = %q, want void", voided.Status)
	}

	entries, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].InvoiceLineID != nil {
		t.Fatalf("entry still locked after voiding draft: %+v", entries)
	}

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

// TestRunBillingAudits covers Ruling 28: a billing run writes a
// "billing_run" audit_log row against its created invoice, carrying the
// actor set via billing.WithActor and a diff with client_contact_id,
// from, to and the billed entry ids.
func TestRunBillingAudits(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	userID := e.User("entries-audit@example.com").ID
	ownerID := e.User("owner-audit@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	entry := e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 60}, from)

	b := e.BillingService()
	actorCtx := billing.WithActor(ctx, ownerID)
	result, err := timesheets.RunBilling(actorCtx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14,
	})
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}

	var actor int64
	var diff []byte
	if err := pool.QueryRow(ctx,
		`SELECT actor_user_id, diff FROM audit_log WHERE org_id = $1 AND entity = 'invoice' AND entity_id = $2 AND action = 'billing_run'`,
		org.ID, result.Invoice.ID,
	).Scan(&actor, &diff); err != nil {
		t.Fatalf("selecting billing_run audit row: %v", err)
	}
	if actor != ownerID {
		t.Fatalf("actor_user_id = %d, want %d", actor, ownerID)
	}

	var parsed struct {
		ClientContactID int64   `json:"client_contact_id"`
		From            string  `json:"from"`
		To              string  `json:"to"`
		EntryIDs        []int64 `json:"entry_ids"`
	}
	if err := json.Unmarshal(diff, &parsed); err != nil {
		t.Fatalf("decoding diff: %v", err)
	}
	if parsed.ClientContactID != client || parsed.From != "2026-11-01" || parsed.To != "2026-11-30" {
		t.Fatalf("diff = %+v, want client %d, from 2026-11-01, to 2026-11-30", parsed, client)
	}
	if len(parsed.EntryIDs) != 1 || parsed.EntryIDs[0] != entry.ID {
		t.Fatalf("diff.EntryIDs = %v, want [%d]", parsed.EntryIDs, entry.ID)
	}
}

func TestRunBillingIssueFlag(t *testing.T) {
	e := testkit.New(t)
	ctx := e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.PayOptions())
	userID := e.User("entries-issue@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 60}, from)

	b := e.BillingService()
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

// TestRunBillingZeroTotal422 covers Ruling 28: a billing run whose summed
// invoice total would be zero (a 1-cent rate and a 1-minute entry:
// DivRound(1*1, 60) rounds the line to $0.00) is rejected with
// core.ErrInvalidInput before anything is committed - no draft invoice and
// no locked entries - rather than creating a draft core.IssueInvoice would
// then refuse to issue.
//
// This replaces the former TestRunBillingIssueFailureLeavesDraft, which
// used this same zero-total setup to drive a *post-commit* Issue failure;
// now that RunBilling itself rejects a zero total before committing, that
// trigger no longer reaches Issue at all. No other realistic trigger
// remains for a timesheet billing run's Issue step: Source "timesheet" is
// exempt from the QLD pay-options rule (only "rent"/"bond" are checked),
// and core.IssueInvoice's only other failure modes (not found, not a
// draft) aren't reachable immediately after RunBilling creates the draft
// itself. Forcing the failure via a cancelled context isn't viable either:
// RunBilling uses the same ctx throughout, so a context cancelled early
// enough to fail Issue would also fail the billing transaction that must
// create the draft in the first place. Per the controller's documented
// fallback, the RunResult-with-error-after-commit path (RunBilling
// returning a populated result with a non-nil error after the billing
// transaction already committed) is left covered by
// TestRunBillingIssueFailureLeavesDraft's git history and this comment
// alone; see docs/superpowers/plans/2026-11-05-01-followups.md's "Plan 3
// leftovers" section.
func TestRunBillingZeroTotal422(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	userID := e.User("entries-zero-total@example.com").ID
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client, testkit.ProjectName("Pro bono"), testkit.ProjectRate(1), testkit.ProjectGST("GST"))
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-30")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: from, Minutes: 1}, from)

	b := e.BillingService()
	result, err := timesheets.RunBilling(ctx, b, org.ID, "acme", timesheets.RunInput{
		ClientContactID: client, From: from, To: to, DueInDays: 14, Issue: true,
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("RunBilling: err = %v, want ErrInvalidInput", err)
	}
	if result.Invoice.ID != 0 {
		t.Fatalf("result.Invoice.ID = %d, want 0 (no draft committed)", result.Invoice.ID)
	}

	invoices, _, err := core.ListInvoices(ctx, pool, org.ID, core.InvoiceListFilter{})
	if err != nil {
		t.Fatalf("ListInvoices: %v", err)
	}
	if len(invoices) != 0 {
		t.Fatalf("invoices = %+v, want none created", invoices)
	}

	entries, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].InvoiceLineID != nil {
		t.Fatalf("entries = %+v, want the one entry left unbilled", entries)
	}
}

// --- HTTP route tests ---

func TestBillingRunRouteResponseShape(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))
	e.Entry(org, owner.ID, timesheets.Entry{
		TaskID: task.ID, WorkDate: testkit.Date("2026-11-01"), Minutes: 60,
	}, testkit.Date("2026-11-05"))

	body := map[string]any{
		"client_contact_id": client, "from": "2026-11-01", "to": "2026-11-30", "due_in_days": 14,
	}
	rr := s.Do(http.MethodPost, "/api/v1/orgs/acme/billing-runs", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rr.Code, rr.Body)
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body, &resp); err != nil {
		t.Fatalf("decoding response: %v, body=%s", err, rr.Body)
	}
	for _, key := range []string{"invoice", "entry_count", "total_minutes"} {
		if _, ok := resp[key]; !ok {
			t.Fatalf("response missing top-level key %q: body=%s", key, rr.Body)
		}
	}

	var invoice map[string]json.RawMessage
	if err := json.Unmarshal(resp["invoice"], &invoice); err != nil {
		t.Fatalf("decoding invoice: %v", err)
	}
	for _, key := range []string{"id", "contact_id", "total", "subtotal", "gst_total", "pay_options", "lines"} {
		if _, ok := invoice[key]; !ok {
			t.Fatalf("invoice missing snake_case key %q: body=%s", key, rr.Body)
		}
	}
	for _, key := range []string{"OrgID", "org_id", "ProviderRef", "provider_ref", "ProviderURL", "provider_url", "DueOn", "due_on", "DueInDays", "due_in_days"} {
		if _, ok := invoice[key]; ok {
			t.Fatalf("invoice leaks internal key %q: body=%s", key, rr.Body)
		}
	}

	var lines []map[string]json.RawMessage
	if err := json.Unmarshal(invoice["lines"], &lines); err != nil {
		t.Fatalf("decoding invoice.lines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("len(invoice.lines) = %d, want 1", len(lines))
	}
	if _, ok := lines[0]["unit_amount"]; !ok {
		t.Fatalf("lines[0] missing unit_amount: body=%s", rr.Body)
	}
}
