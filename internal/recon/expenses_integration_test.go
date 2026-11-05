//go:build integration

package recon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/property"
	"shigoto/internal/recon"
	"shigoto/internal/timesheets"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %s: %v", s, err)
	}
	return d
}

func setGSTRegistered(t *testing.T, pool *pgxpool.Pool, orgID int64, registered bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "UPDATE orgs SET gst_registered = $1 WHERE id = $2", registered, orgID); err != nil {
		t.Fatalf("setting gst_registered: %v", err)
	}
}

func testResidentialProperty(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	p, err := property.CreateProperty(context.Background(), pool, orgID, property.Property{Name: "1 Test St", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

func testProject(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	clientID, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: "client", Name: "Test Client"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	proj, err := timesheets.CreateProject(context.Background(), pool, orgID, timesheets.Project{
		ClientContactID: clientID.ID, Name: "Test Project", HourlyRate: 10000, GSTCode: core.GST,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return proj.ID
}

func categoryByName(t *testing.T, pool *pgxpool.Pool, orgID int64, name string) recon.Category {
	t.Helper()
	cats, err := recon.ListCategories(context.Background(), pool, orgID)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	for _, c := range cats {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("category %q not found among %d categories", name, len(cats))
	return recon.Category{}
}

// TestCreateOrgSeedsCategories covers the 15 default expense categories
// core.CreateOrg seeds, including the one capital category (Equipment).
func TestCreateOrgSeedsCategories(t *testing.T) {
	pool := db.TestPool(t)
	org := testOrg(t, pool, "acme")

	cats, err := recon.ListCategories(context.Background(), pool, org.ID)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(cats) != 15 {
		t.Fatalf("len(categories) = %d, want 15", len(cats))
	}
	for _, c := range cats {
		if c.Type != "expense" {
			t.Errorf("category %q type = %q, want expense", c.Name, c.Type)
		}
		if c.Name == "Equipment" && !c.Capital {
			t.Errorf("Equipment.Capital = false, want true")
		}
		if c.Name != "Equipment" && c.Capital {
			t.Errorf("category %q.Capital = true, want false", c.Name)
		}
	}
}

// TestExpenseSplitLinesGST covers a two-line split expense where one line
// is GST and the other is GST-free: GST is per-line, and the lines' amounts
// sum to the expense total.
func TestExpenseSplitLinesGST(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	setGSTRegistered(t, pool, org.ID, true)

	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
	water := categoryByName(t, pool, org.ID, "Water")

	exp, warnings, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-01"),
		Description: "Plumber + water bill",
		Lines: []recon.NewExpenseLine{
			{CategoryID: repairs.ID, Amount: 11000},
			{CategoryID: water.ID, Amount: 5500},
		},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(exp.Lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(exp.Lines))
	}
	if exp.Lines[0].GSTAmount != 1000 {
		t.Errorf("repairs gst_amount = %d, want 1000", exp.Lines[0].GSTAmount)
	}
	if exp.Lines[1].GSTAmount != 0 {
		t.Errorf("water gst_amount = %d, want 0", exp.Lines[1].GSTAmount)
	}
	total := exp.Lines[0].Amount + exp.Lines[1].Amount
	if total != 16500 {
		t.Errorf("expense total = %d, want 16500", total)
	}
}

// TestExpenseResidentialForcedINP covers a line tagged to a residential
// property: it's stored as INP with zero GST regardless of the category's
// own default, and the response carries the input-taxed warning exactly
// once even when more than one line is residential.
func TestExpenseResidentialForcedINP(t *testing.T) {
	const wantWarning = "residential property expenses are input-taxed; GST credit not claimed"

	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	setGSTRegistered(t, pool, org.ID, true)
	propertyID := testResidentialProperty(t, pool, org.ID)
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
	water := categoryByName(t, pool, org.ID, "Water")

	exp, warnings, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-01"),
		Description: "Plumber + water, both residential",
		Lines: []recon.NewExpenseLine{
			{CategoryID: repairs.ID, Amount: 11000, PropertyID: &propertyID},
			{CategoryID: water.ID, Amount: 5500, PropertyID: &propertyID},
		},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one even with two residential lines", warnings)
	}
	if warnings[0] != wantWarning {
		t.Errorf("warnings[0] = %q, want %q", warnings[0], wantWarning)
	}
	for i, line := range exp.Lines {
		if line.GSTCode != core.INP {
			t.Errorf("line %d gst_code = %s, want INP", i, line.GSTCode)
		}
		if line.GSTAmount != 0 {
			t.Errorf("line %d gst_amount = %d, want 0", i, line.GSTAmount)
		}
	}
}

// TestExpenseUnregisteredOrgNoGST covers an org that isn't GST-registered:
// every expense line has zero GST even for a GST-coded category.
func TestExpenseUnregisteredOrgNoGST(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme") // gst_registered defaults to false
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")

	exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-01"),
		Description: "Plumber",
		Lines: []recon.NewExpenseLine{
			{CategoryID: repairs.ID, Amount: 11000},
		},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}
	if exp.Lines[0].GSTAmount != 0 {
		t.Errorf("gst_amount = %d, want 0 (org not GST-registered)", exp.Lines[0].GSTAmount)
	}
}

// TestExpenseCrossOrgCategory422 covers a category belonging to another
// org: it must be treated as not found, same as a bogus id.
func TestExpenseCrossOrgCategory422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	other := testOrg(t, pool, "other")
	otherCategory := categoryByName(t, pool, other.ID, "Repairs & maintenance")

	_, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-01"),
		Description: "Plumber",
		Lines: []recon.NewExpenseLine{
			{CategoryID: otherCategory.ID, Amount: 11000},
		},
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want core.ErrInvalidInput", err)
	}
}

// TestExpenseLineNoDefaultGSTCode422 covers a line whose category has no
// default_gst_code and no explicit override: CreateCategory/UpdateCategory
// reject an empty default going forward, but a category created before
// that rule (or some other write path) could still have one, so
// CreateExpense itself must still reject the resulting empty gst_code
// rather than storing it.
func TestExpenseLineNoDefaultGSTCode422(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")

	var categoryID int64
	if err := pool.QueryRow(ctx,
		"INSERT INTO categories (org_id, name, type, default_gst_code) VALUES ($1, $2, 'expense', NULL) RETURNING id",
		org.ID, "Legacy no-default",
	).Scan(&categoryID); err != nil {
		t.Fatalf("inserting legacy category: %v", err)
	}

	_, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-01"),
		Description: "Something",
		Lines: []recon.NewExpenseLine{
			{CategoryID: categoryID, Amount: 11000},
		},
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want core.ErrInvalidInput (422)", err)
	}
}

// TestDeleteLinkedExpense409 covers DeleteExpense refusing to delete an
// expense reconciled against a bank line.
func TestDeleteLinkedExpense409(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()
	org := testOrg(t, pool, "acme")
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")

	exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     mustDate(t, "2026-11-01"),
		Description: "Plumber",
		Lines: []recon.NewExpenseLine{
			{CategoryID: repairs.ID, Amount: 11000},
		},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}

	account := testBankAccount(t, pool, org.ID, "Everyday", commbankMapping())
	_, err = recon.Import(ctx, pool, org.ID, account.ID, bytes.NewReader([]byte(fixtureCSV)))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	lines, err := recon.ListBankLines(ctx, pool, org.ID, recon.BankLineFilter{})
	if err != nil {
		t.Fatalf("ListBankLines: %v", err)
	}
	if len(lines) == 0 {
		t.Fatalf("no bank lines imported")
	}
	if _, err := pool.Exec(ctx, "UPDATE expenses SET bank_line_id = $1 WHERE id = $2", lines[0].ID, exp.ID); err != nil {
		t.Fatalf("linking expense to bank line: %v", err)
	}

	if err := recon.DeleteExpense(ctx, pool, org.ID, exp.ID); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("err = %v, want core.ErrInvalidState", err)
	}
}

// --- file upload tests (HTTP level, via recon's routes) ---

func uploadFile(handler http.Handler, token string, data []byte, filename string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		panic(err)
	}
	if _, err := part.Write(data); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/acme/files", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// pngBytes is a minimal valid 1x1 PNG, enough for http.DetectContentType
// to sniff it as image/png.
var pngBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

type fileResponseBody struct {
	ID int64 `json:"id"`
}

func TestFileUploadSniffAndDedupe(t *testing.T) {
	_, handler, token, _ := reconTestSetup(t)

	rr := uploadFile(handler, token, pngBytes, "receipt.png")
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var first fileResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	rr = uploadFile(handler, token, pngBytes, "receipt-again.png")
	if rr.Code != http.StatusCreated {
		t.Fatalf("second upload: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var second fileResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &second); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second upload id = %d, want same as first (%d)", second.ID, first.ID)
	}

	rr = uploadFile(handler, token, []byte("not an image"), "notes.txt")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("text upload: status = %d, want 422, body = %s", rr.Code, rr.Body.String())
	}
}

// TestFileDownloadOwnFileHeaders covers the download response's security
// headers and the sanitized Content-Disposition filename: the upload's
// original name carries a quote (which sanitizeFilename must strip) to
// prove the full upload-then-download round trip sanitizes it, not just
// the unit-level sanitizeFilename cases in routes_test.go.
func TestFileDownloadOwnFileHeaders(t *testing.T) {
	_, handler, token, _ := reconTestSetup(t)

	rr := uploadFile(handler, token, pngBytes, `My "Receipt".png`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var uploaded fileResponseBody
	if err := json.Unmarshal(rr.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/files/%d", uploaded.ID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("download: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	want := `attachment; filename="My Receipt.png"`
	if got := rr.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if rr.Body.Len() != len(pngBytes) {
		t.Errorf("downloaded %d bytes, want %d", rr.Body.Len(), len(pngBytes))
	}
}

func TestFileDownloadOtherOrg404(t *testing.T) {
	pool, handler, token, _ := reconTestSetup(t)
	other := testOrg(t, pool, "other")
	otherFile, err := recon.SaveFile(context.Background(), pool, other.ID, t.TempDir(), "receipt.png", bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/files/%d", otherFile.ID), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}
