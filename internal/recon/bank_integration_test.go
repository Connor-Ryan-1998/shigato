//go:build integration

// Package recon_test exercises the recon store and HTTP handlers (module
// gating, upload limits, cross-org access), so it needs to import httpapi
// and auth alongside recon without an import cycle.
package recon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/importers"
	"shigoto/internal/recon"
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

func commbankMapping() importers.CSVMapping {
	m, _ := importers.Preset("commbank")
	return m
}

func testBankAccount(t *testing.T, pool *pgxpool.Pool, orgID int64, name string, m importers.CSVMapping) recon.BankAccount {
	t.Helper()
	a, err := recon.CreateBankAccount(context.Background(), pool, orgID, name, m)
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	return a
}

func newReconTestHandler(t *testing.T, pool *pgxpool.Pool, enabled bool) http.Handler {
	t.Helper()
	a := auth.New(pool, false)
	return httpapi.New(httpapi.Deps{
		Pool:   pool,
		Auth:   a,
		Cfg:    config.Config{Modules: map[string]bool{"reconciliation": enabled}, FilesDir: t.TempDir()},
		GetEnv: func(string) string { return "" },
	})
}

// reconTestSetup builds an org with a logged-in owner and a handler with
// the reconciliation module enabled, for the handler-level tests below.
func reconTestSetup(t *testing.T) (pool *pgxpool.Pool, handler http.Handler, token string, orgID int64) {
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
	return pool, newReconTestHandler(t, pool, true), token, org.ID
}

// uploadCSV POSTs csv as a multipart "file" upload to
// /api/v1/orgs/acme/bank-accounts/{id}/imports.
func uploadCSV(handler http.Handler, token string, accountID int64, csv []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "statement.csv")
	if err != nil {
		panic(err)
	}
	if _, err := part.Write(csv); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}

	path := fmt.Sprintf("/api/v1/orgs/acme/bank-accounts/%d/imports", accountID)
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

const fixtureCSV = "01/03/2026,-45.50,Coffee Shop\n" +
	"02/03/2026,1200.00,Salary\n" +
	"03/03/2026,-12.99,Book Store\n"

type importResponseBody struct {
	RowsTotal int `json:"rows_total"`
	RowsNew   int `json:"rows_new"`
}

func decodeImportResponse(t *testing.T, rr *httptest.ResponseRecorder) importResponseBody {
	t.Helper()
	var body importResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response %s: %v", rr.Body.String(), err)
	}
	return body
}

func TestImportIdempotent(t *testing.T) {
	pool, handler, token, orgID := reconTestSetup(t)
	account := testBankAccount(t, pool, orgID, "Everyday", commbankMapping())

	rr := uploadCSV(handler, token, account.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusOK {
		t.Fatalf("first import: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	first := decodeImportResponse(t, rr)
	if first.RowsTotal != 3 || first.RowsNew != 3 {
		t.Fatalf("first import = %+v, want {RowsTotal:3 RowsNew:3}", first)
	}

	rr = uploadCSV(handler, token, account.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusOK {
		t.Fatalf("second import: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	second := decodeImportResponse(t, rr)
	if second.RowsTotal != 3 || second.RowsNew != 0 {
		t.Fatalf("second (repeat) import = %+v, want {RowsTotal:3 RowsNew:0}", second)
	}

	lines, err := recon.ListBankLines(context.Background(), pool, orgID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (no duplicates persisted)", len(lines))
	}
}

func TestImportOverlap(t *testing.T) {
	pool, handler, token, orgID := reconTestSetup(t)
	account := testBankAccount(t, pool, orgID, "Everyday", commbankMapping())

	rr := uploadCSV(handler, token, account.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusOK {
		t.Fatalf("first import: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	overlap := fixtureCSV + "04/03/2026,-9.00,Newsagent\n"
	rr = uploadCSV(handler, token, account.ID, []byte(overlap))
	if rr.Code != http.StatusOK {
		t.Fatalf("overlap import: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got := decodeImportResponse(t, rr)
	if got.RowsTotal != 4 || got.RowsNew != 1 {
		t.Fatalf("overlap import = %+v, want {RowsTotal:4 RowsNew:1}", got)
	}

	lines, err := recon.ListBankLines(context.Background(), pool, orgID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("len(lines) = %d, want 4", len(lines))
	}
}

// TestImportKeepsSameDayDuplicates covers two genuinely identical
// transactions on the same statement (same date, amount and description):
// both must be kept, since DedupeKeys distinguishes them by occurrence
// index rather than collapsing them as duplicates.
func TestImportKeepsSameDayDuplicates(t *testing.T) {
	pool, handler, token, orgID := reconTestSetup(t)
	account := testBankAccount(t, pool, orgID, "Everyday", commbankMapping())

	csv := "01/03/2026,-5.00,Coffee\n01/03/2026,-5.00,Coffee\n"
	rr := uploadCSV(handler, token, account.ID, []byte(csv))
	if rr.Code != http.StatusOK {
		t.Fatalf("import: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got := decodeImportResponse(t, rr)
	if got.RowsTotal != 2 || got.RowsNew != 2 {
		t.Fatalf("import = %+v, want {RowsTotal:2 RowsNew:2}", got)
	}

	lines, err := recon.ListBankLines(context.Background(), pool, orgID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (both identical transactions kept)", len(lines))
	}
}

func TestImportCrossOrgAccount404(t *testing.T) {
	pool, handler, token, _ := reconTestSetup(t)
	other := testOrg(t, pool, "other")
	otherAccount := testBankAccount(t, pool, other.ID, "Other's Account", commbankMapping())

	rr := uploadCSV(handler, token, otherAccount.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}

func TestImportTooLarge413(t *testing.T) {
	pool, handler, token, orgID := reconTestSetup(t)
	account := testBankAccount(t, pool, orgID, "Everyday", commbankMapping())

	big := bytes.Repeat([]byte("01/03/2026,-1.00,Padding\n"), 300000) // well over 5 MiB
	rr := uploadCSV(handler, token, account.ID, big)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", rr.Code, rr.Body.String())
	}
}

func TestReconModuleDisabled404(t *testing.T) {
	pool, _, token, _ := reconTestSetup(t)
	handler := newReconTestHandler(t, pool, false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/bank-accounts", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}

func TestCreateBankAccountRequiresExactlyOneMappingStyle(t *testing.T) {
	_, handler, token, _ := reconTestSetup(t)

	rr := postJSON(handler, token, "/api/v1/orgs/acme/bank-accounts", map[string]any{
		"name": "Both styles", "preset": "commbank",
		"csv_mapping": commbankMapping(),
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("both set: status = %d, want 422, body = %s", rr.Code, rr.Body.String())
	}

	rr = postJSON(handler, token, "/api/v1/orgs/acme/bank-accounts", map[string]any{"name": "Neither style"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("neither set: status = %d, want 422, body = %s", rr.Code, rr.Body.String())
	}
}

func TestCreateBankAccountViaPreset(t *testing.T) {
	_, handler, token, _ := reconTestSetup(t)
	rr := postJSON(handler, token, "/api/v1/orgs/acme/bank-accounts", map[string]any{
		"name": "Everyday", "preset": "commbank",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", rr.Code, rr.Body.String())
	}
}

// TestImportConcurrentSameFile covers the controller's correctness
// requirement for concurrent imports of the same file: it's the unique
// constraint on (bank_account_id, dedupe_key), not an app-level check,
// that decides which rows land. Two goroutines import the identical bytes
// at the same time; across both calls, RowsNew must sum to exactly the
// row count once - every row created by exactly one of the two imports,
// never both, never neither.
func TestImportConcurrentSameFile(t *testing.T) {
	pool, _, _, orgID := reconTestSetup(t)
	account := testBankAccount(t, pool, orgID, "Everyday", commbankMapping())

	const n = 2
	results := make([]recon.ImportResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = recon.Import(context.Background(), pool, orgID, account.ID, bytes.NewReader([]byte(fixtureCSV)))
		}(i)
	}
	wg.Wait()

	sumNew := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Import[%d]: %v", i, errs[i])
		}
		if results[i].RowsTotal != 3 {
			t.Fatalf("Import[%d].RowsTotal = %d, want 3", i, results[i].RowsTotal)
		}
		sumNew += results[i].RowsNew
	}
	if sumNew != 3 {
		t.Fatalf("sum of RowsNew across concurrent imports = %d, want 3 (each row created exactly once)", sumNew)
	}

	lines, err := recon.ListBankLines(context.Background(), pool, orgID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (no duplicates persisted)", len(lines))
	}
}

func postJSON(handler http.Handler, token, path string, body any) *httptest.ResponseRecorder {
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// TestImportRouteAuditsActor covers that a route-triggered import records
// the uploading user as the bank_import audit entry's actor, not NULL.
func TestImportRouteAuditsActor(t *testing.T) {
	pool, handler, token, orgID := reconTestSetup(t)
	account := testBankAccount(t, pool, orgID, "Everyday", commbankMapping())
	if rr := uploadCSV(handler, token, account.ID, []byte(fixtureCSV)); rr.Code != http.StatusOK {
		t.Fatalf("import: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var actorID *int64
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_user_id FROM audit_log
		WHERE org_id = $1 AND entity = 'bank_import' AND action = 'create'`,
		orgID,
	).Scan(&actorID); err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	if actorID == nil {
		t.Fatalf("audit_log actor_user_id = nil, want the uploading user's id")
	}
}
