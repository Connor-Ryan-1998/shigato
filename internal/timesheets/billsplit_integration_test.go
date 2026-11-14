//go:build integration

package timesheets_test

import (
	"context"
	"errors"
	"testing"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/testkit"
	"shigoto/internal/timesheets"
)

// selectAll echoes every task in preview back as the run's selection.
func selectAll(preview timesheets.Preview) []timesheets.TaskSelection {
	var out []timesheets.TaskSelection
	for _, p := range preview.Projects {
		for _, tk := range p.Tasks {
			out = append(out, timesheets.TaskSelection{TaskID: tk.TaskID, Minutes: tk.Minutes, Amount: tk.Amount})
		}
	}
	return out
}

// previewFor previews in's client and range, failing the test on error.
func previewFor(t *testing.T, ctx context.Context, b *billing.Service, orgID int64, in timesheets.RunInput) timesheets.Preview {
	t.Helper()
	preview, err := timesheets.PreviewBilling(ctx, b.Pool, orgID, timesheets.PreviewInput{ClientContactID: in.ClientContactID, From: in.From, To: in.To})
	if err != nil {
		t.Fatalf("PreviewBilling: %v", err)
	}
	return preview
}

// billAll previews in's range and bills everything in it as one combined
// invoice: the old single-invoice billing run.
func billAll(t *testing.T, ctx context.Context, b *billing.Service, orgID int64, in timesheets.RunInput) (timesheets.RunResult, error) {
	t.Helper()
	in.Split = "combined"
	in.Tasks = selectAll(previewFor(t, ctx, b, orgID, in))
	return timesheets.RunBilling(ctx, b, orgID, "acme", in)
}

func countRows(t *testing.T, e *testkit.Env, table, where string, args ...any) int {
	t.Helper()
	return e.Count(table, where, args...)
}

func TestPreviewMatchesInvoiceToTheCent(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	userID := e.User("preview@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	proj := e.Project(org, client, testkit.ProjectName("Site"), testkit.ProjectRate(12000), testkit.ProjectGST("GST"))
	task := e.Task(org, proj.ID, testkit.TaskName("Design"))
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: day, Minutes: 50}, day)

	b := e.BillingService()
	in := timesheets.RunInput{ClientContactID: client, From: testkit.Date("2026-11-01"), To: testkit.Date("2026-11-30"), DueInDays: 14}
	preview := previewFor(t, e.Ctx, b, org.ID, in)
	if len(preview.Projects) != 1 || len(preview.Projects[0].Tasks) != 1 {
		t.Fatalf("preview = %+v, want one project with one task", preview)
	}
	got := preview.Projects[0].Tasks[0]
	if got.TaskID != task.ID || got.TaskName != "Design" || got.Minutes != 50 || got.Amount != 10000 || got.GSTAmount != 1000 {
		t.Fatalf("preview task = %+v, want Design 50 min, 10000 + 1000 GST", got)
	}
	if preview.Projects[0].ProjectName != "Site" {
		t.Fatalf("project name = %q, want Site", preview.Projects[0].ProjectName)
	}

	result, err := billAll(t, e.Ctx, b, org.ID, in)
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if total := result.Invoices[0].Invoice.Total; total != 11000 {
		t.Fatalf("invoice total = %d, want 11000", total)
	}
}

func TestRunBillingSelectedTasksOnly(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	userID := e.User("select@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	proj := e.Project(org, client)
	keep := e.Task(org, proj.ID, testkit.TaskName("Build"))
	skip := e.Task(org, proj.ID, testkit.TaskName("Disputed"))
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: keep.ID, WorkDate: day, Minutes: 60}, day)
	e.Entry(org, userID, timesheets.Entry{TaskID: skip.ID, WorkDate: day, Minutes: 30}, day)

	b := e.BillingService()
	in := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14, Split: "combined"}
	for _, s := range selectAll(previewFor(t, e.Ctx, b, org.ID, in)) {
		if s.TaskID == keep.ID {
			in.Tasks = append(in.Tasks, s)
		}
	}
	result, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in)
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if result.EntryCount != 1 || result.TotalMinutes != 60 || len(result.Invoices) != 1 || len(result.Invoices[0].Invoice.Lines) != 1 {
		t.Fatalf("result = %+v, want one invoice billing only the 60-minute task", result)
	}
	left, err := timesheets.ListEntries(e.Ctx, e.Pool, org.ID, timesheets.EntryFilter{TaskID: &skip.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].InvoiceLineID != nil {
		t.Fatalf("unselected task's entry = %+v, want unbilled", left)
	}
}

func TestRunBillingPerProject(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	userID := e.User("split@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	beta := e.Project(org, client, testkit.ProjectName("Beta"), testkit.ProjectRate(6000))
	alpha := e.Project(org, client, testkit.ProjectName("Alpha"), testkit.ProjectRate(12000))
	ta := e.Task(org, alpha.ID, testkit.TaskName("A1"))
	tb := e.Task(org, beta.ID, testkit.TaskName("B1"))
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: ta.ID, WorkDate: day, Minutes: 60}, day)
	e.Entry(org, userID, timesheets.Entry{TaskID: tb.ID, WorkDate: day, Minutes: 60}, day)

	b := e.BillingService()
	in := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14, Split: "project"}
	in.Tasks = selectAll(previewFor(t, e.Ctx, b, org.ID, in))
	result, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in)
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if len(result.Invoices) != 2 {
		t.Fatalf("invoices = %d, want 2", len(result.Invoices))
	}
	if a, bb := result.Invoices[0].Invoice, result.Invoices[1].Invoice; a.Total != 12000 || bb.Total != 6000 || len(a.Lines) != 1 || len(bb.Lines) != 1 {
		t.Fatalf("invoices = %+v / %+v, want Alpha 12000 then Beta 6000, one line each", a, bb)
	}
	for _, ri := range result.Invoices {
		if n := countRows(t, e, "audit_log", "org_id=$1 AND entity='invoice' AND entity_id=$2 AND action='billing_run' AND diff->>'split'='project'", org.ID, ri.Invoice.ID); n != 1 {
			t.Fatalf("billing_run audit rows for invoice %d = %d, want 1", ri.Invoice.ID, n)
		}
	}
}

func TestRunBillingStaleSelection(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	userID := e.User("stale@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	proj := e.Project(org, client)
	selected := e.Task(org, proj.ID, testkit.TaskName("Build"))
	other := e.Task(org, proj.ID, testkit.TaskName("Other"))
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: selected.ID, WorkDate: day, Minutes: 60}, day)
	e.Entry(org, userID, timesheets.Entry{TaskID: other.ID, WorkDate: day, Minutes: 60}, day)

	b := e.BillingService()
	in := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14, Split: "combined"}
	for _, s := range selectAll(previewFor(t, e.Ctx, b, org.ID, in)) {
		if s.TaskID == selected.ID {
			in.Tasks = append(in.Tasks, s)
		}
	}

	// Time logged on an unselected task after the preview doesn't matter.
	e.Entry(org, userID, timesheets.Entry{TaskID: other.ID, WorkDate: day, Minutes: 15}, day)
	// Time logged on the selected task does.
	extra := e.Entry(org, userID, timesheets.Entry{TaskID: selected.ID, WorkDate: day, Minutes: 10}, day)

	_, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in)
	if !errors.Is(err, core.ErrInvalidState) || err.Error() != "billing preview changed; refresh and review" {
		t.Fatalf("err = %v, want ErrInvalidState preview changed", err)
	}
	if n := countRows(t, e, "invoices", "org_id=$1", org.ID); n != 0 {
		t.Fatalf("invoices = %d, want 0", n)
	}
	if n := countRows(t, e, "audit_log", "org_id=$1 AND action='billing_run'", org.ID); n != 0 {
		t.Fatalf("billing_run audit rows = %d, want 0", n)
	}

	// Removing the extra entry restores the previewed state; the run then succeeds.
	if _, err := e.Pool.Exec(e.Ctx, `DELETE FROM time_entries WHERE id = $1`, extra.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in); err != nil {
		t.Fatalf("RunBilling after unselected-only change: %v", err)
	}
}

func TestRunBillingRetryAfterCommitIs409(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	userID := e.User("retry@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	task := e.Task(org, e.Project(org, client).ID, testkit.TaskName("Build"))
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: day, Minutes: 60}, day)

	b := e.BillingService()
	in := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14, Split: "combined"}
	in.Tasks = selectAll(previewFor(t, e.Ctx, b, org.ID, in))
	if _, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("retried run: err = %v, want ErrInvalidState", err)
	}
	if n := countRows(t, e, "invoices", "org_id=$1", org.ID); n != 1 {
		t.Fatalf("invoices = %d, want 1", n)
	}
}

func TestRunBillingRejectsBadSelection(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	foreignOrg := e.Org("other")
	userID := e.User("bad@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	task := e.Task(org, e.Project(org, client).ID, testkit.TaskName("Build"))
	foreignTask := e.Task(foreignOrg, e.Project(foreignOrg, e.Contact(foreignOrg).ID).ID)
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: task.ID, WorkDate: day, Minutes: 60}, day)

	b := e.BillingService()
	base := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14, Split: "combined"}
	sel := timesheets.TaskSelection{TaskID: task.ID, Minutes: 60, Amount: 12000}

	cases := map[string]timesheets.RunInput{}
	empty := base
	cases["empty"] = empty
	dup := base
	dup.Tasks = []timesheets.TaskSelection{sel, sel}
	cases["duplicate"] = dup
	split := base
	split.Split = "weekly"
	split.Tasks = []timesheets.TaskSelection{sel}
	cases["split"] = split
	for name, in := range cases {
		if _, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}

	foreign := base
	foreign.Tasks = []timesheets.TaskSelection{{TaskID: foreignTask.ID, Minutes: 60, Amount: 12000}}
	if _, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", foreign); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("foreign task: err = %v, want ErrInvalidState", err)
	}
	if n := countRows(t, e, "invoices", "org_id=$1", org.ID); n != 0 {
		t.Fatalf("invoices = %d, want 0", n)
	}
}

func TestRunBillingPerProjectIssueFailureReportsEach(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	userID := e.User("issuefail@example.com").ID
	client := e.Contact(org, testkit.Name("Client")).ID
	ta := e.Task(org, e.Project(org, client, testkit.ProjectName("Alpha")).ID, testkit.TaskName("A1"))
	tb := e.Task(org, e.Project(org, client, testkit.ProjectName("Beta")).ID, testkit.TaskName("B1"))
	day := testkit.Date("2026-11-05")
	e.Entry(org, userID, timesheets.Entry{TaskID: ta.ID, WorkDate: day, Minutes: 60}, day)
	e.Entry(org, userID, timesheets.Entry{TaskID: tb.ID, WorkDate: day, Minutes: 60}, day)

	calls := 0
	restore := timesheets.SetIssueForTest(func(s *billing.Service, ctx context.Context, orgID int64, slug string, id int64) (core.Invoice, error) {
		calls++
		if calls == 2 {
			return core.Invoice{}, errors.New("issue exploded")
		}
		return s.Issue(ctx, orgID, slug, id)
	})
	defer restore()

	b := e.BillingService()
	in := timesheets.RunInput{ClientContactID: client, From: day, To: day, DueInDays: 14, Split: "project", Issue: true}
	in.Tasks = selectAll(previewFor(t, e.Ctx, b, org.ID, in))
	result, err := timesheets.RunBilling(e.Ctx, b, org.ID, "acme", in)
	if err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	if len(result.Invoices) != 2 {
		t.Fatalf("invoices = %d, want 2", len(result.Invoices))
	}
	if result.Invoices[0].IssueErr != nil || result.Invoices[0].Invoice.Status != "open" {
		t.Fatalf("first invoice = %+v, want issued", result.Invoices[0])
	}
	if result.Invoices[1].IssueErr == nil || result.Invoices[1].Invoice.Status != "draft" {
		t.Fatalf("second invoice = %+v, want draft with issue error", result.Invoices[1])
	}
	if n := countRows(t, e, "invoices", "org_id=$1", org.ID); n != 2 {
		t.Fatalf("invoices = %d, want 2", n)
	}
}
