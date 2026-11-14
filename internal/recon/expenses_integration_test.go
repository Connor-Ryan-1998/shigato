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
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
	"shigoto/internal/recon"
	"shigoto/internal/testkit"
)

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
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")

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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))

	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
	water := categoryByName(t, pool, org.ID, "Water")

	exp, warnings, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     testkit.Date("2026-11-01"),
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

	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	propertyID := e.Property(org, testkit.Residential()).ID
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
	water := categoryByName(t, pool, org.ID, "Water")

	exp, warnings, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     testkit.Date("2026-11-01"),
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme") // gst_registered defaults to false
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")

	exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     testkit.Date("2026-11-01"),
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	other := e.Org("other")
	otherCategory := categoryByName(t, pool, other.ID, "Repairs & maintenance")

	_, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     testkit.Date("2026-11-01"),
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")

	var categoryID int64
	if err := pool.QueryRow(ctx,
		"INSERT INTO categories (org_id, name, type, default_gst_code) VALUES ($1, $2, 'expense', NULL) RETURNING id",
		org.ID, "Legacy no-default",
	).Scan(&categoryID); err != nil {
		t.Fatalf("inserting legacy category: %v", err)
	}

	_, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     testkit.Date("2026-11-01"),
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
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")

	exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn:     testkit.Date("2026-11-01"),
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
	lines, _, err := recon.ListBankLines(ctx, pool, org.ID, recon.BankLineFilter{})
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

func uploadFile(s testkit.Session, data []byte, filename string) testkit.Response {
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

	return s.DoWith(http.MethodPost, "/api/v1/orgs/acme/files", &body, map[string]string{"Content-Type": w.FormDataContentType()})
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
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())

	rr := uploadFile(s, pngBytes, "receipt.png")
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: status = %d, body = %s", rr.Code, rr.Body)
	}
	var first fileResponseBody
	if err := json.Unmarshal(rr.Body, &first); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	rr = uploadFile(s, pngBytes, "receipt-again.png")
	if rr.Code != http.StatusCreated {
		t.Fatalf("second upload: status = %d, body = %s", rr.Code, rr.Body)
	}
	var second fileResponseBody
	if err := json.Unmarshal(rr.Body, &second); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second upload id = %d, want same as first (%d)", second.ID, first.ID)
	}

	rr = uploadFile(s, []byte("not an image"), "notes.txt")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("text upload: status = %d, want 422, body = %s", rr.Code, rr.Body)
	}
}

// TestFileDownloadOwnFileHeaders covers the download response's security
// headers and the sanitized Content-Disposition filename: the upload's
// original name carries a quote (which sanitizeFilename must strip) to
// prove the full upload-then-download round trip sanitizes it, not just
// the unit-level sanitizeFilename cases in routes_test.go.
func TestFileDownloadOwnFileHeaders(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())

	rr := uploadFile(s, pngBytes, `My "Receipt".png`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: status = %d, body = %s", rr.Code, rr.Body)
	}
	var uploaded fileResponseBody
	if err := json.Unmarshal(rr.Body, &uploaded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	rr = s.Do(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/files/%d", uploaded.ID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("download: status = %d, body = %s", rr.Code, rr.Body)
	}
	if got := rr.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	want := `attachment; filename="My Receipt.png"`
	if got := rr.Header.Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if len(rr.Body) != len(pngBytes) {
		t.Errorf("downloaded %d bytes, want %d", len(rr.Body), len(pngBytes))
	}
}

func TestFileDownloadOtherOrg404(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	other := e.Org("other")
	otherFile, err := core.SaveFile(context.Background(), pool, other.ID, t.TempDir(), "receipt.png", bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	rr := s.Do(http.MethodGet, fmt.Sprintf("/api/v1/orgs/acme/files/%d", otherFile.ID), nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body)
	}
}

// TestDeleteFeeExpense409 covers that a "Stripe fees" expense MatchPayout
// booked (referenced by payouts.fee_expense_id) can't be deleted directly:
// it's a 409 telling the user to unmatch the payout, not a 500 from the
// foreign key.
func TestDeleteFeeExpense409(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx
	org := e.Org("acme")
	e.Settings(org, testkit.GSTRegistered(true))
	_, feeExpenseID := matchFeePayout(e, org)

	err := module.RunInTx(ctx, pool, func(tx pgx.Tx) error {
		return recon.DeleteExpense(ctx, tx, org.ID, feeExpenseID)
	})
	wantPublicConflict(t, err, "this is a Stripe payout fee; unmatch the payout instead")
	if _, err := recon.GetExpense(ctx, pool, org.ID, feeExpenseID); err != nil {
		t.Fatalf("fee expense after rejected delete: %v", err)
	}
}

// waitUntilBlockedBy polls until some backend is waiting on a lock held by
// backend pid, so a test can commit pid's transaction knowing the other
// request has already reached its lock.
func waitUntilBlockedBy(ctx context.Context, pool *pgxpool.Pool, pid uint32) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, int32(pid)).Scan(&blocked); err == nil && blocked {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestDeleteExpenseRacesMatchExpense covers DeleteExpense and MatchExpense
// racing on the same expense, in both orders: the first transaction holds
// its locks until the second is provably waiting on them, then commits.
// Either way the result is consistent - linked and not deleted, or deleted
// with the line still unmatched - never a deleted expense on a "matched"
// line.
func TestDeleteExpenseRacesMatchExpense(t *testing.T) {
	for _, first := range []string{"match", "delete"} {
		t.Run(first+" first", func(t *testing.T) {
			e := testkit.New(t)
			pool, ctx := e.Pool, e.Ctx
			org := e.Org("acme")
			repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
			exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
				SpentOn: testkit.Date("2026-11-01"), Description: "Plumber",
				Lines: []recon.NewExpenseLine{{CategoryID: repairs.ID, Amount: 11000}},
			})
			if err != nil {
				t.Fatalf("CreateExpense: %v", err)
			}
			lineID := testBankLineID(t, pool, org.ID, -11000, testkit.Date("2026-11-01"), "Plumber")

			match := func(tx pgx.Tx) error { return recon.MatchExpense(ctx, tx, org.ID, lineID, exp.ID) }
			del := func(tx pgx.Tx) error { return recon.DeleteExpense(ctx, tx, org.ID, exp.ID) }
			firstFn, secondFn := match, del
			if first == "delete" {
				firstFn, secondFn = del, match
			}

			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback(ctx)
			if err := firstFn(tx); err != nil {
				t.Fatalf("first %s: %v", first, err)
			}

			done := make(chan error, 1)
			go func() { done <- module.RunInTx(ctx, pool, secondFn) }()
			if !waitUntilBlockedBy(ctx, pool, tx.Conn().PgConn().PID()) {
				tx.Rollback(ctx)
				<-done
				t.Fatalf("second request never blocked on the first's locks")
			}
			if err := tx.Commit(ctx); err != nil {
				<-done
				t.Fatalf("commit: %v", err)
			}
			secondErr := <-done

			status, _ := bankLineStatus(t, pool, lineID)
			stored, getErr := recon.GetExpense(ctx, pool, org.ID, exp.ID)
			if first == "match" {
				if !errors.Is(secondErr, core.ErrInvalidState) {
					t.Fatalf("delete after match: err = %v, want ErrInvalidState", secondErr)
				}
				if getErr != nil || stored.BankLineID == nil || *stored.BankLineID != lineID || status != "matched" {
					t.Fatalf("want linked and kept: expense err=%v link=%v, line status %s", getErr, stored.BankLineID, status)
				}
				return
			}
			if !errors.Is(secondErr, core.ErrNotFound) {
				t.Fatalf("match after delete: err = %v, want ErrNotFound", secondErr)
			}
			if !errors.Is(getErr, core.ErrNotFound) || status != "unmatched" {
				t.Fatalf("want deleted and line unmatched: expense err=%v, line status %s", getErr, status)
			}
		})
	}
}

// TestDeleteExpenseRouteAuditsContents covers that deleting an expense
// leaves an audit entry (with the requesting user as actor) recording what
// was deleted - date, description and every line - since the rows are
// gone afterwards.
func TestDeleteExpenseRouteAuditsContents(t *testing.T) {
	e := testkit.New(t)
	pool := e.Pool
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	ctx := e.Ctx
	e.Settings(org, testkit.GSTRegistered(true))
	repairs := categoryByName(t, pool, org.ID, "Repairs & maintenance")
	exp, _, err := recon.CreateExpense(ctx, pool, org.ID, recon.NewExpense{
		SpentOn: testkit.Date("2026-11-01"), Description: "Plumber",
		Lines: []recon.NewExpenseLine{
			{CategoryID: repairs.ID, Amount: 11000},
			{CategoryID: repairs.ID, Amount: 500, GSTCode: ptrGST(core.FRE)},
		},
	})
	if err != nil {
		t.Fatalf("CreateExpense: %v", err)
	}

	rr := s.Do(http.MethodDelete, fmt.Sprintf("/api/v1/orgs/acme/expenses/%d", exp.ID), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body)
	}

	rows, err := pool.Query(ctx, `SELECT actor_user_id, diff FROM audit_log WHERE org_id = $1 AND entity = 'expense' AND entity_id = $2 AND action = 'delete'`, org.ID, exp.ID)
	if err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	var actors []*int64
	var diff []byte
	for rows.Next() {
		var actor *int64
		if err := rows.Scan(&actor, &diff); err != nil {
			t.Fatalf("scanning audit_log: %v", err)
		}
		actors = append(actors, actor)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit_log rows: %v", err)
	}
	if want := owner.ID; len(actors) != 1 || actors[0] == nil || *actors[0] != want {
		t.Fatalf("delete audit actors = %v, want exactly one with the requesting user %d", actors, want)
	}
	want := fmt.Sprintf(`{"spent_on":"2026-11-01","description":"Plumber","lines":[`+
		`{"category_id":%d,"amount":11000,"gst_code":"GST","gst_amount":1000},`+
		`{"category_id":%d,"amount":500,"gst_code":"FRE","gst_amount":0}]}`, repairs.ID, repairs.ID)
	var got, wantJSON any
	if err := json.Unmarshal(diff, &got); err != nil {
		t.Fatalf("decoding diff %s: %v", diff, err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatalf("decoding want: %v", err)
	}
	if !reflect.DeepEqual(got, wantJSON) {
		t.Fatalf("diff = %s, want %s", diff, want)
	}
}

func ptrGST(c core.GSTCode) *core.GSTCode { return &c }
