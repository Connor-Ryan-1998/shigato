//go:build integration

// Package recon_test exercises the recon store and HTTP handlers (upload
// limits, cross-org access), so it needs to import httpapi and auth
// alongside recon without an import cycle.
package recon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/importers"
	"shigoto/internal/recon"
	"shigoto/internal/testkit"
)

func commbankMapping() importers.CSVMapping {
	m, _ := importers.Preset("commbank")
	return m
}

func testBankAccount(t testing.TB, pool *pgxpool.Pool, orgID int64, name string, m importers.CSVMapping) recon.BankAccount {
	t.Helper()
	a, err := recon.CreateBankAccount(context.Background(), pool, orgID, name, m)
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	return a
}

// uploadCSV POSTs csv as a multipart "file" upload to
// /api/v1/orgs/acme/bank-accounts/{id}/imports.
func uploadCSV(s testkit.Session, accountID int64, csv []byte) testkit.Response {
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
	return s.DoWith(http.MethodPost, path, &body, map[string]string{"Content-Type": w.FormDataContentType()})
}

const fixtureCSV = "01/03/2026,-45.50,Coffee Shop\n" +
	"02/03/2026,1200.00,Salary\n" +
	"03/03/2026,-12.99,Book Store\n"

type importResponseBody struct {
	RowsTotal int `json:"rows_total"`
	RowsNew   int `json:"rows_new"`
}

func decodeImportResponse(t *testing.T, rr testkit.Response) importResponseBody {
	t.Helper()
	var body importResponseBody
	if err := json.Unmarshal(rr.Body, &body); err != nil {
		t.Fatalf("decoding response %s: %v", rr.Body, err)
	}
	return body
}

func TestImportIdempotent(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())

	rr := uploadCSV(s, account.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusOK {
		t.Fatalf("first import: status = %d, body = %s", rr.Code, rr.Body)
	}
	first := decodeImportResponse(t, rr)
	if first.RowsTotal != 3 || first.RowsNew != 3 {
		t.Fatalf("first import = %+v, want {RowsTotal:3 RowsNew:3}", first)
	}

	rr = uploadCSV(s, account.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusOK {
		t.Fatalf("second import: status = %d, body = %s", rr.Code, rr.Body)
	}
	second := decodeImportResponse(t, rr)
	if second.RowsTotal != 3 || second.RowsNew != 0 {
		t.Fatalf("second (repeat) import = %+v, want {RowsTotal:3 RowsNew:0}", second)
	}

	lines, _, err := recon.ListBankLines(context.Background(), pool, org.ID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (no duplicates persisted)", len(lines))
	}
}

func TestImportOverlap(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())

	rr := uploadCSV(s, account.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusOK {
		t.Fatalf("first import: status = %d, body = %s", rr.Code, rr.Body)
	}

	overlap := fixtureCSV + "04/03/2026,-9.00,Newsagent\n"
	rr = uploadCSV(s, account.ID, []byte(overlap))
	if rr.Code != http.StatusOK {
		t.Fatalf("overlap import: status = %d, body = %s", rr.Code, rr.Body)
	}
	got := decodeImportResponse(t, rr)
	if got.RowsTotal != 4 || got.RowsNew != 1 {
		t.Fatalf("overlap import = %+v, want {RowsTotal:4 RowsNew:1}", got)
	}

	lines, _, err := recon.ListBankLines(context.Background(), pool, org.ID, recon.BankLineFilter{})
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
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())

	csv := "01/03/2026,-5.00,Coffee\n01/03/2026,-5.00,Coffee\n"
	rr := uploadCSV(s, account.ID, []byte(csv))
	if rr.Code != http.StatusOK {
		t.Fatalf("import: status = %d, body = %s", rr.Code, rr.Body)
	}
	got := decodeImportResponse(t, rr)
	if got.RowsTotal != 2 || got.RowsNew != 2 {
		t.Fatalf("import = %+v, want {RowsTotal:2 RowsNew:2}", got)
	}

	lines, _, err := recon.ListBankLines(context.Background(), pool, org.ID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (both identical transactions kept)", len(lines))
	}
}

func TestImportCrossOrgAccount404(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	other := e.Org("other")
	otherAccount := testBankAccount(t, pool, other.ID, "Other's Account", commbankMapping())

	rr := uploadCSV(s, otherAccount.ID, []byte(fixtureCSV))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body)
	}
}

func TestImportTooLarge413(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())

	big := bytes.Repeat([]byte("01/03/2026,-1.00,Padding\n"), 300000) // well over 5 MiB
	rr := uploadCSV(s, account.ID, big)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", rr.Code, rr.Body)
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
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())

	const n = 2
	results := make([]recon.ImportResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = recon.Import(context.Background(), pool, org.ID, account.ID, bytes.NewReader([]byte(fixtureCSV)))
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

	lines, _, err := recon.ListBankLines(context.Background(), pool, org.ID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (no duplicates persisted)", len(lines))
	}
}

// TestImportRouteAuditsActor covers that a route-triggered import records
// the uploading user as the bank_import audit entry's actor, not NULL.
func TestImportRouteAuditsActor(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())
	if rr := uploadCSV(s, account.ID, []byte(fixtureCSV)); rr.Code != http.StatusOK {
		t.Fatalf("import: status = %d, body = %s", rr.Code, rr.Body)
	}

	var actorID *int64
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_user_id FROM audit_log
		WHERE org_id = $1 AND entity = 'bank_import' AND action = 'create'`,
		org.ID,
	).Scan(&actorID); err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	if want := owner.ID; actorID == nil || *actorID != want {
		t.Fatalf("audit_log actor_user_id = %v, want the uploading user's id %d", actorID, want)
	}
}
