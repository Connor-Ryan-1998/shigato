//go:build integration

// Package timesheets_test exercises the timesheets store, plus one test
// that goes through the real HTTP handlers (module gating), so it needs to
// import httpapi and auth alongside timesheets without an import cycle.
package timesheets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/timesheets"
)

// sessionCookieName mirrors the unexported constant of the same name in
// package auth (internal/auth/session.go); there's no exported way to get
// it from here.
const sessionCookieName = "shigoto_session"

func testOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func testContact(t *testing.T, pool *pgxpool.Pool, orgID int64, kind, name string) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: kind, Name: name})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func testProject(t *testing.T, pool *pgxpool.Pool, orgID, clientID int64) timesheets.Project {
	t.Helper()
	p, err := timesheets.CreateProject(context.Background(), pool, orgID, timesheets.Project{
		ClientContactID: clientID, Name: "Website rebuild", HourlyRate: 12000, GSTCode: core.GST,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p
}

func TestCreateProjectRequiresClientContact(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")

	tenant := testContact(t, pool, org.ID, "tenant", "A Tenant")
	if _, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: tenant, Name: "Bad client kind", HourlyRate: 10000, GSTCode: core.GST,
	}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("project with tenant contact: err = %v, want ErrInvalidInput", err)
	}

	other := testOrg(t, pool, "other")
	otherClient := testContact(t, pool, other.ID, "client", "Other Org Client")
	if _, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: otherClient, Name: "Cross org client", HourlyRate: 10000, GSTCode: core.GST,
	}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("project with cross-org client: err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateProjectDuplicateName422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")

	if _, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: client, Name: "Website rebuild", HourlyRate: 10000, GSTCode: core.GST,
	}); err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	if _, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: client, Name: "Website rebuild", HourlyRate: 15000, GSTCode: core.GST,
	}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("duplicate name: err = %v, want ErrInvalidInput", err)
	}
}

func TestUpdateProjectDeactivate(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)

	updated, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, timesheets.Project{
		Name: proj.Name, HourlyRate: proj.HourlyRate, GSTCode: proj.GSTCode, Active: false,
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if updated.Active {
		t.Fatalf("Active = true, want false")
	}
	if updated.ClientContactID != client {
		t.Fatalf("ClientContactID = %d, want unchanged %d", updated.ClientContactID, client)
	}

	got, err := timesheets.GetProject(ctx, pool, org.ID, proj.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Active {
		t.Fatalf("persisted Active = true, want false")
	}
}

func TestCreateTaskCrossOrgProject404(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)

	other := testOrg(t, pool, "other")
	if _, err := timesheets.CreateTask(ctx, pool, other.ID, proj.ID, timesheets.Task{Name: "Design"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-org CreateTask: err = %v, want ErrNotFound", err)
	}
}

func TestCreateTaskDuplicateName422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)

	if _, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Design"}); err != nil {
		t.Fatalf("first CreateTask: %v", err)
	}
	if _, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Design"}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("duplicate task name: err = %v, want ErrInvalidInput", err)
	}
}

// TestEffectiveRate is a pure unit test (no DB needed), kept alongside the
// store tests above rather than in its own file per the plan.
func TestEffectiveRate(t *testing.T) {
	project := timesheets.Project{HourlyRate: 10000}

	noOverride := timesheets.Task{HourlyRate: nil}
	if got := noOverride.EffectiveRate(project); got != project.HourlyRate {
		t.Fatalf("EffectiveRate with no override = %d, want project rate %d", got, project.HourlyRate)
	}

	override := core.Cents(15000)
	withOverride := timesheets.Task{HourlyRate: &override}
	if got := withOverride.EffectiveRate(project); got != override {
		t.Fatalf("EffectiveRate with override = %d, want override %d", got, override)
	}
}

func newTimesheetsTestHandler(pool *pgxpool.Pool, enabled bool) http.Handler {
	a := auth.New(pool, false)
	return httpapi.New(httpapi.Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{"timesheets": enabled}},
		GetEnv: func(string) string { return "" },
	})
}

// timesheetsTestSetup builds an org with a logged-in owner and a handler
// with the timesheets module enabled, for the handler-level tests below.
func timesheetsTestSetup(t *testing.T) (pool *pgxpool.Pool, handler http.Handler, token string, orgID int64) {
	t.Helper()
	pool = db.TestPool(t)
	org := testOrg(t, pool, "acme")

	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		"owner@example.com", "Owner", hash,
	).Scan(&userID); err != nil {
		t.Fatalf("inserting user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		userID, org.ID, string(auth.Owner),
	); err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	token, err = auth.Login(context.Background(), pool, "owner@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return pool, newTimesheetsTestHandler(pool, true), token, org.ID
}

func putJSON(handler http.Handler, token, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestTimesheetsModuleDisabled404(t *testing.T) {
	pool, _, token, _ := timesheetsTestSetup(t)
	handler := newTimesheetsTestHandler(pool, false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/projects", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestUpdateProjectRequiresAllFields covers the controller ruling that
// PUT /projects/{id} is a full replace: an omitted field must 422, never
// silently default (an omitted "active" defaulting to true could
// re-activate a deactivated project).
func TestUpdateProjectRequiresAllFields(t *testing.T) {
	pool, handler, token, orgID := timesheetsTestSetup(t)
	client := testContact(t, pool, orgID, "client", "A Client")
	proj := testProject(t, pool, orgID, client)
	path := fmt.Sprintf("/api/v1/orgs/acme/projects/%d", proj.ID)

	full := map[string]any{
		"name": proj.Name, "hourly_rate": int64(proj.HourlyRate),
		"gst_code": string(proj.GSTCode), "active": true,
	}

	withoutActive := map[string]any{"name": full["name"], "hourly_rate": full["hourly_rate"], "gst_code": full["gst_code"]}
	if rr := putJSON(handler, token, path, withoutActive); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing active: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}

	withoutGST := map[string]any{"name": full["name"], "hourly_rate": full["hourly_rate"], "active": full["active"]}
	if rr := putJSON(handler, token, path, withoutGST); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing gst_code: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}

// TestUpdateTaskHourlyRateKeyRequired covers the controller ruling that
// PUT /tasks/{id} must include the hourly_rate key (null to clear the
// override), never treat an omitted key as "leave the override alone".
func TestUpdateTaskHourlyRateKeyRequired(t *testing.T) {
	pool, handler, token, orgID := timesheetsTestSetup(t)
	client := testContact(t, pool, orgID, "client", "A Client")
	proj := testProject(t, pool, orgID, client)
	task, err := timesheets.CreateTask(context.Background(), pool, orgID, proj.ID, timesheets.Task{Name: "Design"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	missingRate := map[string]any{"name": "Design", "active": true}
	rr := putJSON(handler, token, fmt.Sprintf("/api/v1/orgs/acme/tasks/%d", task.ID), missingRate)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing hourly_rate key: status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}

// TestUpdateTaskNullHourlyRateClearsOverride covers explicit JSON null
// clearing a task's rate override.
func TestUpdateTaskNullHourlyRateClearsOverride(t *testing.T) {
	pool, handler, token, orgID := timesheetsTestSetup(t)
	client := testContact(t, pool, orgID, "client", "A Client")
	proj := testProject(t, pool, orgID, client)
	rate := core.Cents(5000)
	task, err := timesheets.CreateTask(context.Background(), pool, orgID, proj.ID, timesheets.Task{Name: "Design", HourlyRate: &rate})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	body := map[string]any{"name": "Design", "active": true, "hourly_rate": nil}
	rr := putJSON(handler, token, fmt.Sprintf("/api/v1/orgs/acme/tasks/%d", task.ID), body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		HourlyRate *int64 `json:"hourly_rate"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.HourlyRate != nil {
		t.Fatalf("response hourly_rate = %v, want nil (cleared)", *resp.HourlyRate)
	}

	got, err := timesheets.GetTask(context.Background(), pool, orgID, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.HourlyRate != nil {
		t.Fatalf("persisted HourlyRate = %v, want nil (cleared)", *got.HourlyRate)
	}
}
