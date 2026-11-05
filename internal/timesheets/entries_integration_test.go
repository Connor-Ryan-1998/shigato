//go:build integration

package timesheets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/timesheets"
)

func mustParseDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %q: %v", s, err)
	}
	return d
}

func TestCreateEntryValidation(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	inactiveTask, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Dormant"})
	if err != nil {
		t.Fatalf("CreateTask(inactive): %v", err)
	}
	if _, err := timesheets.UpdateTask(ctx, pool, org.ID, inactiveTask.ID, timesheets.Task{Name: inactiveTask.Name, Active: false}); err != nil {
		t.Fatalf("UpdateTask(deactivate): %v", err)
	}

	inactiveProj := testProject2(t, pool, org.ID, client, "Dormant project")
	if _, err := timesheets.UpdateProject(ctx, pool, org.ID, inactiveProj.ID, timesheets.Project{
		Name: inactiveProj.Name, HourlyRate: inactiveProj.HourlyRate, GSTCode: inactiveProj.GSTCode, Active: false,
	}); err != nil {
		t.Fatalf("UpdateProject(deactivate): %v", err)
	}
	taskInInactiveProj, err := timesheets.CreateTask(ctx, pool, org.ID, inactiveProj.ID, timesheets.Task{Name: "Orphaned"})
	if err != nil {
		t.Fatalf("CreateTask(in inactive project): %v", err)
	}

	other := testOrg(t, pool, "other")
	otherClient := testContact(t, pool, other.ID, "client", "Other Client")
	otherProj := testProject(t, pool, other.ID, otherClient)
	otherTask, err := timesheets.CreateTask(ctx, pool, other.ID, otherProj.ID, timesheets.Task{Name: "Other"})
	if err != nil {
		t.Fatalf("CreateTask(other org): %v", err)
	}

	userID := testUser(t, pool, "owner@example.com")
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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	userID := testUser(t, pool, "note-limit@example.com")
	today := mustParseDate(t, "2026-11-05")

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
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	userID := testUser(t, pool, "update-same-task@example.com")
	today := mustParseDate(t, "2026-11-05")

	entry, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task.ID, WorkDate: today, Minutes: 30,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	if _, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, timesheets.Project{
		Name: proj.Name, HourlyRate: proj.HourlyRate, GSTCode: proj.GSTCode, Active: false,
	}); err != nil {
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
	otherTask, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Other"})
	if err != nil {
		t.Fatalf("CreateTask(otherTask): %v", err)
	}
	if _, err := timesheets.UpdateEntry(ctx, pool, org.ID, actor, entry.ID, timesheets.Entry{
		TaskID: otherTask.ID, WorkDate: today, Minutes: 90, Note: "revised",
	}, today); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("UpdateEntry moving to a task in an inactive project: err = %v, want ErrInvalidInput", err)
	}
}

// testProject2 creates a second project with a distinct name, since
// testProject (projects_integration_test.go) always uses "Website rebuild".
func testProject2(t *testing.T, pool *pgxpool.Pool, orgID, clientID int64, name string) timesheets.Project {
	t.Helper()
	p, err := timesheets.CreateProject(context.Background(), pool, orgID, timesheets.Project{
		ClientContactID: clientID, Name: name, HourlyRate: 12000, GSTCode: core.GST,
	})
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", name, err)
	}
	return p
}

func testUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	var id int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		email, email, hash,
	).Scan(&id); err != nil {
		t.Fatalf("inserting user: %v", err)
	}
	return id
}

func addMembership(t *testing.T, pool *pgxpool.Pool, userID, orgID int64, role auth.Role) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, orgID, string(role),
	); err != nil {
		t.Fatalf("adding membership: %v", err)
	}
}

func postJSON(handler http.Handler, token, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func deleteReq(handler http.Handler, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestStaffCannotEditOthersEntry(t *testing.T) {
	pool, handler, ownerToken, orgID := timesheetsTestSetup(t)
	ctx := context.Background()
	client := testContact(t, pool, orgID, "client", "A Client")
	proj := testProject(t, pool, orgID, client)
	task, err := timesheets.CreateTask(ctx, pool, orgID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	var ownerID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", "owner@example.com").Scan(&ownerID); err != nil {
		t.Fatalf("looking up owner id: %v", err)
	}
	staffID := testUser(t, pool, "staff@example.com")
	addMembership(t, pool, staffID, orgID, auth.Staff)
	staffToken, err := auth.Login(ctx, pool, "staff@example.com", "correct-password")
	if err != nil {
		t.Fatalf("staff Login: %v", err)
	}

	ownerEntry, err := timesheets.CreateEntry(ctx, pool, orgID, ownerID, timesheets.Entry{
		TaskID: task.ID, WorkDate: mustParseDate(t, "2026-11-01"), Minutes: 60,
	}, mustParseDate(t, "2026-11-05"))
	if err != nil {
		t.Fatalf("CreateEntry(owner): %v", err)
	}
	staffEntry, err := timesheets.CreateEntry(ctx, pool, orgID, staffID, timesheets.Entry{
		TaskID: task.ID, WorkDate: mustParseDate(t, "2026-11-01"), Minutes: 45,
	}, mustParseDate(t, "2026-11-05"))
	if err != nil {
		t.Fatalf("CreateEntry(staff): %v", err)
	}

	update := map[string]any{"task_id": task.ID, "work_date": "2026-11-02", "minutes": 90, "note": "updated"}

	if rr := putJSON(handler, staffToken, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", ownerEntry.ID), update); rr.Code != http.StatusForbidden {
		t.Fatalf("staff editing owner's entry: status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
	if rr := putJSON(handler, staffToken, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", staffEntry.ID), update); rr.Code != http.StatusOK {
		t.Fatalf("staff editing own entry: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if rr := putJSON(handler, ownerToken, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", staffEntry.ID), update); rr.Code != http.StatusOK {
		t.Fatalf("owner editing staff's entry: status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
}

func TestBilledEntryLocked(t *testing.T) {
	pool, handler, token, orgID := timesheetsTestSetup(t)
	ctx := context.Background()
	client := testContact(t, pool, orgID, "client", "A Client")
	proj := testProject(t, pool, orgID, client)
	task, err := timesheets.CreateTask(ctx, pool, orgID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var ownerID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", "owner@example.com").Scan(&ownerID); err != nil {
		t.Fatalf("looking up owner id: %v", err)
	}
	entry, err := timesheets.CreateEntry(ctx, pool, orgID, ownerID, timesheets.Entry{
		TaskID: task.ID, WorkDate: mustParseDate(t, "2026-11-01"), Minutes: 60,
	}, mustParseDate(t, "2026-11-05"))
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	inv, err := core.CreateDraftInvoice(ctx, pool, orgID, core.NewInvoice{
		ContactID: client, Source: "manual", DueInDays: 14,
		Lines: []core.NewLine{{Description: "Build", QuantityMilli: 1000, UnitAmount: 12000, GSTCode: "GST"}},
	})
	if err != nil {
		t.Fatalf("CreateDraftInvoice: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE time_entries SET invoice_line_id = $1 WHERE id = $2", inv.Lines[0].ID, entry.ID); err != nil {
		t.Fatalf("marking entry billed: %v", err)
	}

	update := map[string]any{"task_id": task.ID, "work_date": "2026-11-02", "minutes": 90, "note": "updated"}
	if rr := putJSON(handler, token, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", entry.ID), update); rr.Code != http.StatusConflict {
		t.Fatalf("PUT billed entry: status = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
	if rr := deleteReq(handler, token, fmt.Sprintf("/api/v1/orgs/acme/time-entries/%d", entry.ID)); rr.Code != http.StatusConflict {
		t.Fatalf("DELETE billed entry: status = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
}

func TestListEntriesFilterAndTotal(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj1 := testProject(t, pool, org.ID, client)
	proj2 := testProject2(t, pool, org.ID, client, "Second project")
	task1, err := timesheets.CreateTask(ctx, pool, org.ID, proj1.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask(task1): %v", err)
	}
	task2, err := timesheets.CreateTask(ctx, pool, org.ID, proj2.ID, timesheets.Task{Name: "Design"})
	if err != nil {
		t.Fatalf("CreateTask(task2): %v", err)
	}
	userID := testUser(t, pool, "owner@example.com")
	today := mustParseDate(t, "2026-11-10")

	e1, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task1.ID, WorkDate: mustParseDate(t, "2026-11-01"), Minutes: 60,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry(e1): %v", err)
	}
	e2, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task1.ID, WorkDate: mustParseDate(t, "2026-11-03"), Minutes: 90,
	}, today)
	if err != nil {
		t.Fatalf("CreateEntry(e2): %v", err)
	}
	e3, err := timesheets.CreateEntry(ctx, pool, org.ID, userID, timesheets.Entry{
		TaskID: task2.ID, WorkDate: mustParseDate(t, "2026-11-05"), Minutes: 30,
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

	// Inclusive date range covering e1 and e2 only (excludes e3's Nov 5).
	from := mustParseDate(t, "2026-11-01")
	to := mustParseDate(t, "2026-11-03")
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

// TestCreateEntryHandlerUsesCallerAsUser covers the controller ruling that
// POST /time-entries always logs time under the caller, ignoring any
// user_id the body might try to set (the request shape doesn't even accept
// one).
func TestCreateEntryHandlerUsesCallerAsUser(t *testing.T) {
	pool, handler, token, orgID := timesheetsTestSetup(t)
	ctx := context.Background()
	client := testContact(t, pool, orgID, "client", "A Client")
	proj := testProject(t, pool, orgID, client)
	task, err := timesheets.CreateTask(ctx, pool, orgID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var ownerID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", "owner@example.com").Scan(&ownerID); err != nil {
		t.Fatalf("looking up owner id: %v", err)
	}

	body := map[string]any{"task_id": task.ID, "work_date": "2026-11-01", "minutes": 60, "note": "work"}
	rr := postJSON(handler, token, "/api/v1/orgs/acme/time-entries", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.UserID != ownerID {
		t.Fatalf("user_id = %d, want caller's id %d", resp.UserID, ownerID)
	}
}

// TestListEntriesUnbilledInvalid422 covers the controller ruling that
// unbilled accepts only "true"; any other value is a 422, not a silently
// ignored filter.
func TestListEntriesUnbilledInvalid422(t *testing.T) {
	_, handler, token, _ := timesheetsTestSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/time-entries?unbilled=yes", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}
