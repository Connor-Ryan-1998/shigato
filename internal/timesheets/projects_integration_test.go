//go:build integration

// Package timesheets_test exercises the timesheets store, plus a few tests
// that go through the real HTTP handlers, so it needs to import httpapi
// and auth alongside timesheets without an import cycle.
package timesheets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// withStatus returns p with Status replaced, for a full-replace
// UpdateProject call that only changes the status.
func withStatus(p timesheets.Project, status string) timesheets.Project {
	p.Status = status
	return p
}

func TestCreateProjectDefaultsAndDetails(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")

	proj := testProject(t, pool, org.ID, client)
	if proj.Status != timesheets.StatusInProgress || proj.BillingMethod != timesheets.BillingTM || !proj.Active {
		t.Fatalf("defaults = %q/%q/active=%v, want in_progress/tm/true", proj.Status, proj.BillingMethod, proj.Active)
	}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	p, err := timesheets.CreateProject(ctx, pool, org.ID, timesheets.Project{
		ClientContactID: client, Name: "Fixed job", HourlyRate: 15000, GSTCode: core.GST,
		Status: timesheets.StatusPending, Description: "Scope doc", StartDate: &start, EndDate: &end,
		BillingMethod: timesheets.BillingFixed,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	got, err := timesheets.GetProject(ctx, pool, org.ID, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Status != timesheets.StatusPending || !got.Active || got.Description != "Scope doc" ||
		got.BillingMethod != timesheets.BillingFixed || !got.StartDate.Equal(start) || !got.EndDate.Equal(end) {
		t.Fatalf("round trip = %+v", got)
	}

	bad := []timesheets.Project{
		{ClientContactID: client, Name: "Bad status", HourlyRate: 1, GSTCode: core.GST, Status: "paused"},
		{ClientContactID: client, Name: "Bad method", HourlyRate: 1, GSTCode: core.GST, BillingMethod: "retainer"},
		{ClientContactID: client, Name: "Bad dates", HourlyRate: 1, GSTCode: core.GST, StartDate: &end, EndDate: &start},
	}
	for _, b := range bad {
		if _, err := timesheets.CreateProject(ctx, pool, org.ID, b); !errors.Is(err, core.ErrInvalidInput) {
			t.Fatalf("%s: err = %v, want ErrInvalidInput", b.Name, err)
		}
	}
}

func TestUpdateProjectStatusDrivesActive(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)

	for status, active := range map[string]bool{
		timesheets.StatusCancelled: false, timesheets.StatusPending: true,
		timesheets.StatusCompleted: false, timesheets.StatusInProgress: true,
	} {
		updated, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, withStatus(proj, status))
		if err != nil {
			t.Fatalf("UpdateProject(%s): %v", status, err)
		}
		if updated.Active != active {
			t.Fatalf("status %s: Active = %v, want %v", status, updated.Active, active)
		}
	}
}

func TestUpdateProjectCustomerLockedOnceBilled(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	other := testContact(t, pool, org.ID, "client", "Other Client")
	tenant := testContact(t, pool, org.ID, "tenant", "A Tenant")
	proj := testProject(t, pool, org.ID, client)

	moved := proj
	moved.ClientContactID = tenant
	if _, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, moved); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("move to tenant: err = %v, want ErrInvalidInput", err)
	}
	moved.ClientContactID = other
	updated, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, moved)
	if err != nil || updated.ClientContactID != other {
		t.Fatalf("move unbilled project: %+v, %v", updated, err)
	}

	// Bill some time, then the customer is locked.
	task, err := timesheets.CreateTask(ctx, pool, org.ID, proj.ID, timesheets.Task{Name: "Build"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	userID := testUser(t, pool, "billed@example.com")
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	testEntry(t, pool, org.ID, userID, task.ID, day, 60)
	if _, err := timesheets.RunBilling(ctx, testBillingService(pool), org.ID, "acme", timesheets.RunInput{ClientContactID: other, From: day, To: day, DueInDays: 14}); err != nil {
		t.Fatalf("RunBilling: %v", err)
	}
	moved.ClientContactID = client
	if _, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, moved); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("move billed project: err = %v, want ErrInvalidState", err)
	}
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

func TestUpdateProjectComplete(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	client := testContact(t, pool, org.ID, "client", "A Client")
	proj := testProject(t, pool, org.ID, client)

	updated, err := timesheets.UpdateProject(ctx, pool, org.ID, proj.ID, withStatus(proj, timesheets.StatusCompleted))
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

func newTimesheetsTestHandler(pool *pgxpool.Pool) http.Handler {
	a := auth.New(pool, false)
	return httpapi.New(httpapi.Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{"timesheets": true}},
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
	return pool, newTimesheetsTestHandler(pool), token, org.ID
}

func putJSON(handler http.Handler, token, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shigoto-CSRF", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}
