//go:build integration

package timesheets_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/testkit"
	"shigoto/internal/timesheets"
)

func TestCreateEntryValidation(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	inactiveTask := e.Task(org, proj.ID, testkit.TaskName("Dormant"))
	if _, err := timesheets.UpdateTask(ctx, pool, org.ID, inactiveTask.ID, timesheets.Task{Name: inactiveTask.Name, Active: false}); err != nil {
		t.Fatalf("UpdateTask(deactivate): %v", err)
	}

	inactiveProj := e.Project(org, client, testkit.ProjectName("Dormant project"))
	if _, err := timesheets.UpdateProject(ctx, pool, org.ID, inactiveProj.ID, withStatus(inactiveProj, timesheets.StatusCompleted)); err != nil {
		t.Fatalf("UpdateProject(deactivate): %v", err)
	}
	taskInInactiveProj := e.Task(org, inactiveProj.ID, testkit.TaskName("Orphaned"))

	other := e.Org("other")
	otherClient := e.Contact(other, testkit.Name("Other Client")).ID
	otherProj := e.Project(other, otherClient)
	otherTask := e.Task(other, otherProj.ID, testkit.TaskName("Other"))

	userID := e.User("owner@example.com").ID
	today := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)
	validDate := today

	cases := []struct {
		name string
		e    timesheets.Entry
	}{
		{"zero minutes", timesheets.Entry{TaskID: task.ID, WorkDate: validDate, Minutes: 0}},
		{"over 1440 minutes", timesheets.Entry{TaskID: task.ID, WorkDate: validDate, Minutes: 1441}},
		{"inactive task", timesheets.Entry{TaskID: inactiveTask.ID, WorkDate: validDate, Minutes: 60}},
		{"active task in inactive project", timesheets.Entry{TaskID: taskInInactiveProj.ID, WorkDate: validDate, Minutes: 60}},
		{"task in another org", timesheets.Entry{TaskID: otherTask.ID, WorkDate: validDate, Minutes: 60}},
		{"work_date tomorrow", timesheets.Entry{TaskID: task.ID, WorkDate: tomorrow, Minutes: 60}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, tc.e, today); !errors.Is(err, core.ErrInvalidInput) {
				t.Fatalf("CreateEntry: err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// TestCreateEntryNoteRuneLimit covers Ruling 29: the note length limit is
// counted in runes (utf8.RuneCountInString), not bytes, so a 1000-rune
// multibyte note (2000+ bytes) is accepted, and 1001 runes is rejected.
func TestCreateEntryNoteRuneLimit(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))
	userID := e.User("note-limit@example.com").ID
	today := testkit.Date("2026-11-05")

	note1000 := strings.Repeat("é", 1000) // 2 bytes per rune, 1000 runes.
	if _, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task.ID, WorkDate: today, Minutes: 60, Note: note1000,
	}, today); err != nil {
		t.Fatalf("CreateEntry with 1000-rune multibyte note: %v", err)
	}

	note1001 := strings.Repeat("é", 1001)
	if _, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task.ID, WorkDate: today, Minutes: 60, Note: note1001,
	}, today); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("CreateEntry with 1001-rune note: err = %v, want ErrInvalidInput", err)
	}
}

// TestUpdateEntrySameTaskSkipsTaskCheck covers Ruling 29: UpdateEntry only
// re-validates the task/project when task_id actually changes, so editing
// an unbilled entry's minutes still works once its project has since been
// deactivated (as long as the entry's task_id isn't moving).
func TestUpdateEntrySameTaskSkipsTaskCheck(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))
	userID := e.User("update-same-task@example.com").ID
	today := testkit.Date("2026-11-05")

	entry, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task.ID, WorkDate: today, Minutes: 30,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	if _, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, withStatus(proj, timesheets.StatusCompleted)); err != nil {
		t.Fatalf("UpdateProject(deactivate): %v", err)
	}

	actor := auth.Principal{UserID: userID, OrgID: org.ID, Role: auth.Owner}
	updated, err := timesheets.UpdateEntry(ctx, pool, org.ID, actor, entry.ID, timesheets.Entry{
		TaskID: task.ID, WorkDate: today, Minutes: 90, Note: "revised",
	}, today)
	if err != nil {
		t.Fatalf("UpdateEntry on deactivated project's task (same task_id): %v", err)
	}
	if updated.Minutes != 90 {
		t.Fatalf("Minutes = %d, want 90", updated.Minutes)
	}

	// Moving it to a different (also now-inactive) task still fails.
	otherTask := e.Task(org, proj.ID, testkit.TaskName("Other"))
	if _, err := timesheets.UpdateEntry(ctx, pool, org.ID, actor, entry.ID, timesheets.Entry{
		TaskID: otherTask.ID, WorkDate: today, Minutes: 90, Note: "revised",
	}, today); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("UpdateEntry moving to a task in an inactive project: err = %v, want ErrInvalidInput", err)
	}
}

func TestStaffCannotEditOthersEntry(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	h := e.Handler()
	ownerSession := e.Login(owner, h)
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))

	staff := e.User("staff@example.com")
	e.Member(staff, org, auth.Staff)
	staffSession := e.Login(staff, h)

	ownerEntry := e.Entry(org, owner.ID, timesheets.Entry{
		TaskID: task.ID, WorkDate: testkit.Date("2026-11-01"), Minutes: 60,
	}, testkit.Date("2026-11-05"))
	staffEntry := e.Entry(org, staff.ID, timesheets.Entry{
		TaskID: task.ID, WorkDate: testkit.Date("2026-11-01"), Minutes: 45,
	}, testkit.Date("2026-11-05"))

	update := map[string]any{"task_id": task.ID, "work_date": "2026-11-02", "minutes": 90, "note": "updated"}

	if rr := staffSession.Do(http.MethodPut, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", ownerEntry.ID), update); rr.Code != http.StatusForbidden {
		t.Fatalf("staff editing owner's entry: status = %d, want 403, body=%s", rr.Code, rr.Body)
	}
	if rr := staffSession.Do(http.MethodPut, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", staffEntry.ID), update); rr.Code != http.StatusOK {
		t.Fatalf("staff editing own entry: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}
	if rr := ownerSession.Do(http.MethodPut, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", staffEntry.ID), update); rr.Code != http.StatusOK {
		t.Fatalf("owner editing staff's entry: status = %d, want 200, body=%s", rr.Code, rr.Body)
	}
}

func TestBilledEntryLocked(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj := e.Project(org, client)
	task := e.Task(org, proj.ID, testkit.TaskName("Build"))
	entry := e.Entry(org, owner.ID, timesheets.Entry{
		TaskID: task.ID, WorkDate: testkit.Date("2026-11-01"), Minutes: 60,
	}, testkit.Date("2026-11-05"))

	// CreateDraftInvoice rejects a GST-coded line in an org that isn't
	// GST-registered; this test only cares about locking a billed entry,
	// so register the org rather than switch the line to FRE/N-T.
	e.Settings(org, testkit.GSTRegistered(true))
	inv := e.Invoice(org, client, testkit.Amount(12000), testkit.InvoiceGST("GST"))
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE time_entries SET invoice_line_id = $1 WHERE id = $2", inv.Lines[0].ID, entry.ID); err != nil {
		t.Fatalf("marking entry billed: %v", err)
	}

	update := map[string]any{"task_id": task.ID, "work_date": "2026-11-02", "minutes": 90, "note": "updated"}
	if rr := s.Do(http.MethodPut, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", entry.ID), update); rr.Code != http.StatusConflict {
		t.Fatalf("PUT billed entry: status = %d, want 409, body=%s", rr.Code, rr.Body)
	}
	if rr := s.Do(http.MethodDelete, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", entry.ID), nil); rr.Code != http.StatusConflict {
		t.Fatalf("DELETE billed entry: status = %d, want 409, body=%s", rr.Code, rr.Body)
	}
}

func TestListEntriesFilterAndTotal(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	client := e.Contact(org, testkit.Name("A Client")).ID
	proj1 := e.Project(org, client)
	proj2 := e.Project(org, client, testkit.ProjectName("Second project"))
	task1 := e.Task(org, proj1.ID, testkit.TaskName("Build"))
	task2 := e.Task(org, proj2.ID, testkit.TaskName("Design"))
	userID := e.User("owner@example.com").ID
	today := testkit.Date("2026-11-10")

	e1, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task1.ID, WorkDate: testkit.Date("2026-11-01"), Minutes: 60,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry(e1): %v", err)
	}
	e2, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task1.ID, WorkDate: testkit.Date("2026-11-03"), Minutes: 90,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry(e2): %v", err)
	}
	e3, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task2.ID, WorkDate: testkit.Date("2026-11-05"), Minutes: 30,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry(e3): %v", err)
	}
	_ = e1
	_ = e2
	_ = e3

	// Filter by task_id: e1 + e2 only.
	byTask, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{TaskID: &task1.ID})
	if err != nil {
		t.Fatalf("ListEntries(task_id): %v", err)
	}
	if len(byTask) != 2 {
		t.Fatalf("ListEntries(task_id) len = %d, want 2", len(byTask))
	}
	total := 0
	for _, e := range byTask {
		total += e.Minutes
	}
	if total != 150 {
		t.Fatalf("ListEntries(task_id) total minutes = %d, want 150", total)
	}

	// Filter by project_id: e3 only.
	byProject, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{ProjectID: &proj2.ID})
	if err != nil {
		t.Fatalf("ListEntries(project_id): %v", err)
	}
	if len(byProject) != 1 || byProject[0].Minutes != 30 {
		t.Fatalf("ListEntries(project_id) = %+v, want single 30-minute entry", byProject)
	}
	if d := byProject[0]; d.ProjectID != proj2.ID || d.ProjectName != proj2.Name || d.TaskName != task2.Name ||
		d.ClientName == "" || d.UserName == "" || d.Rate != proj2.HourlyRate || d.BillingMethod != timesheets.BillingTM {
		t.Fatalf("ListEntries(project_id) details = %+v", d)
	}

	// Nothing billed yet: billed filter is empty, unbilled has everything.
	billed, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{Billed: true})
	if err != nil || len(billed) != 0 {
		t.Fatalf("ListEntries(billed) = %+v, %v; want none", billed, err)
	}
	unbilled, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{Unbilled: true})
	if err != nil || len(unbilled) != 3 {
		t.Fatalf("ListEntries(unbilled) len = %d, %v; want 3", len(unbilled), err)
	}

	// Inclusive date range covering e1 and e2 only (excludes e3's Nov 5).
	from := testkit.Date("2026-11-01")
	to := testkit.Date("2026-11-03")
	byRange, err := timesheets.ListEntries(ctx, pool, org.ID, timesheets.EntryFilter{From: &from, To: &to})
	if err != nil {
		t.Fatalf("ListEntries(date range): %v", err)
	}
	if len(byRange) != 2 {
		t.Fatalf("ListEntries(date range) len = %d, want 2", len(byRange))
	}
	if byRange[0].ID != e1.ID || byRange[1].ID != e2.ID {
		t.Fatalf("ListEntries(date range) order = [%d, %d], want [%d, %d] (by work_date)", byRange[0].ID, byRange[1].ID, e1.ID, e2.ID)
	}
}
