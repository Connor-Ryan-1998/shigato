//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/property"
)

func warningDate(t *testing.T, value string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func warningLease(t *testing.T, pool *pgxpool.Pool, orgID, propertyID int64) property.Lease {
	t.Helper()
	tenant := propertyTestContact(t, pool, orgID, "tenant", "Warning tenant")
	lease, err := property.CreateLease(context.Background(), pool, orgID, property.NewLease{
		PropertyID: propertyID, StartDate: warningDate(t, "2026-01-01"), Frequency: property.Weekly,
		RentAmount: 50000, Tenants: []property.NewLeaseTenant{{ContactID: tenant, Primary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func warningPeriod(t *testing.T, pool *pgxpool.Pool, orgID int64, lease property.Lease, start, end, status string) core.Invoice {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	inv, err := core.CreateDraftInvoice(ctx, tx, orgID, core.NewInvoice{
		ContactID: lease.Tenants[0].ContactID, Source: "rent", DueInDays: 14,
		Lines: []core.NewLine{{Description: "Existing rent", QuantityMilli: 1000, UnitAmount: 50000, GSTCode: "INP", PropertyID: &lease.PropertyID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "draft" {
		inv, err = core.IssueInvoice(ctx, tx, orgID, inv.ID, warningDate(t, start))
		if err != nil {
			t.Fatal(err)
		}
		if status == "paid" {
			_, _, err = core.RecordPayment(ctx, tx, orgID, core.NewPayment{InvoiceID: inv.ID, Amount: inv.Total, PaidOn: warningDate(t, start), Method: "bank_transfer"})
		} else if status != "open" {
			_, err = tx.Exec(ctx, "UPDATE invoices SET status=$1 WHERE id=$2 AND org_id=$3", status, inv.ID, orgID)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO rent_periods(org_id,lease_id,period_start,period_end,amount_due,invoice_id) VALUES($1,$2,$3,$4,$5,$6)", orgID, lease.ID, warningDate(t, start), warningDate(t, end), inv.Total, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return inv
}

func postWarningAction(t *testing.T, handler http.Handler, token string, leaseID int64, action string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/leases/%d/%s", leaseID, action), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shigoto-CSRF", "1")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func warningFinancialSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
		'invoices', (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),
		'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM invoice_lines l),
		'payments', (SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM payments p),
		'periods', (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM rent_periods r))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type warningActionResponse struct {
	ID       int64    `json:"id"`
	Warnings []string `json:"warnings"`
	Affected []struct {
		InvoiceID int64  `json:"invoice_id"`
		Number    *int   `json:"invoice_number"`
		Status    string `json:"status"`
		Start     string `json:"start"`
		End       string `json:"end"`
	} `json:"affected_invoices"`
}

func TestPropertyChangesWarnWithoutRewritingInvoices(t *testing.T) {
	for _, action := range []string{"rent-changes", "charges", "terminate"} {
		t.Run(action, func(t *testing.T) {
			pool, handler, token, orgID, propID := propertyTestSetup(t)
			lease := warningLease(t, pool, orgID, propID)
			statuses := []string{"draft", "open", "paid", "uncollectible", "void"}
			var invoiceIDs []int64
			var numbers []*int
			for i, status := range statuses {
				start := warningDate(t, "2026-01-01").AddDate(0, 0, 7*i)
				inv := warningPeriod(t, pool, orgID, lease, start.Format("2006-01-02"), start.AddDate(0, 0, 6).Format("2006-01-02"), status)
				if status != "void" {
					invoiceIDs = append(invoiceIDs, inv.ID)
					numbers = append(numbers, inv.Number)
				}
			}
			otherLease := warningLease(t, pool, orgID, propID)
			warningPeriod(t, pool, orgID, otherLease, "2026-01-01", "2026-01-07", "open")
			otherOrg := webhookTestOrg(t, pool, "foreign")
			foreignLease := warningLease(t, pool, otherOrg.ID, propertyTestProperty(t, pool, otherOrg.ID))
			warningPeriod(t, pool, otherOrg.ID, foreignLease, "2026-01-01", "2026-01-07", "open")
			if _, err := pool.Exec(context.Background(), "INSERT INTO rent_periods(org_id,lease_id,period_start,period_end,amount_due) VALUES($1,$2,'2026-02-05','2026-02-11',50000)", orgID, lease.ID); err != nil {
				t.Fatal(err)
			}
			if action == "rent-changes" {
				if _, _, err := property.CreateRentChange(context.Background(), pool, orgID, lease.ID, warningDate(t, "2026-01-02"), 52000); err != nil {
					t.Fatal(err)
				}
			}
			before := warningFinancialSnapshot(t, pool)
			var body any
			switch action {
			case "rent-changes":
				body = map[string]any{"effective_date": "2026-01-03", "amount": 55000}
			case "charges":
				body = map[string]any{"description": "Water", "start_date": "2026-01-02", "amount": 1000}
			case "terminate":
				body = map[string]any{"termination_date": "2026-01-02"}
			}
			rr := postWarningAction(t, handler, token, lease.ID, action, body)
			wantStatus := http.StatusCreated
			if action == "terminate" {
				wantStatus = http.StatusOK
			}
			if rr.Code != wantStatus {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var response warningActionResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Warnings) == 0 || len(response.Affected) != 4 {
				t.Fatalf("missing billed-period warning: %s", rr.Body.String())
			}
			if action == "rent-changes" && len(response.Warnings) != 2 {
				t.Fatalf("rent increase warning lost: %v", response.Warnings)
			}
			for i, affected := range response.Affected {
				if affected.InvoiceID != invoiceIDs[i] || affected.Status != statuses[i] || !reflect.DeepEqual(affected.Number, numbers[i]) {
					t.Fatalf("bad affected invoice: %+v", affected)
				}
				wantStart := warningDate(t, "2026-01-01").AddDate(0, 0, 7*i)
				if affected.Start != wantStart.Format("2006-01-02") || affected.End != wantStart.AddDate(0, 0, 6).Format("2006-01-02") {
					t.Fatalf("bad period: %+v", affected)
				}
			}
			if response.ID == 0 {
				t.Fatalf("lost existing flattened response: %s", rr.Body.String())
			}
			if got := warningFinancialSnapshot(t, pool); got != before {
				t.Fatal("lease action rewrote invoice, line, payment or rent period")
			}
			updated, err := property.GetLease(context.Background(), pool, orgID, lease.ID)
			if err != nil {
				t.Fatal(err)
			}
			if action == "rent-changes" && len(updated.RentChanges) != 3 {
				t.Fatal("rent change not saved")
			}
			if action == "charges" && len(updated.Charges) != 1 {
				t.Fatal("charge not saved")
			}
			if action == "terminate" && updated.TerminationDate == nil {
				t.Fatal("termination not saved")
			}
			denied := postWarningAction(t, handler, token, foreignLease.ID, action, body)
			if denied.Code != http.StatusNotFound {
				t.Fatalf("foreign lease: status %d", denied.Code)
			}
		})
	}
}

func TestPropertyWarningQueryFailureRollsBackMutation(t *testing.T) {
	pool, handler, token, orgID, propID := propertyTestSetup(t)
	lease := warningLease(t, pool, orgID, propID)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "ALTER TABLE rent_periods RENAME TO saved_rent_periods"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, "ALTER TABLE saved_rent_periods RENAME TO rent_periods"); err != nil {
			t.Error(err)
		}
	}()
	var before int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_log").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct {
		name string
		body any
	}{
		{"rent-changes", map[string]any{"effective_date": "2026-01-02", "amount": 55000}},
		{"charges", map[string]any{"description": "Water", "start_date": "2026-01-02", "amount": 1000}},
		{"terminate", map[string]any{"termination_date": "2026-01-02"}},
	} {
		rr := postWarningAction(t, handler, token, lease.ID, action.name, action.body)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("%s status %d want 500: %s", action.name, rr.Code, rr.Body.String())
		}
	}
	updated, err := property.GetLease(ctx, pool, orgID, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RentChanges) != 1 || len(updated.Charges) != 0 || updated.TerminationDate != nil {
		t.Fatalf("failed warning query persisted mutation: %+v", updated)
	}
	var after int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_log").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("failed warning query persisted audit record")
	}
}

func TestPropertyWarningDateBoundaries(t *testing.T) {
	cases := []struct {
		name, action string
		body         map[string]any
		nextRate     bool
		want         int
	}{
		{"rent on period end", "rent-changes", map[string]any{"effective_date": "2026-01-07", "amount": 55000}, false, 1},
		{"rent after period", "rent-changes", map[string]any{"effective_date": "2026-01-08", "amount": 55000}, false, 0},
		{"rent stops at next rate", "rent-changes", map[string]any{"effective_date": "2026-01-02", "amount": 55000}, true, 1},
		{"charge starts on period end", "charges", map[string]any{"start_date": "2026-01-07", "end_date": "2026-01-07", "description": "Water", "amount": 1000}, false, 1},
		{"charge ends on period start", "charges", map[string]any{"start_date": "2025-12-31", "end_date": "2026-01-01", "description": "Water", "amount": 1000}, false, 1},
		{"charge after period", "charges", map[string]any{"start_date": "2026-01-08", "description": "Water", "amount": 1000}, false, 0},
		{"charge before period", "charges", map[string]any{"start_date": "2025-12-30", "end_date": "2025-12-31", "description": "Water", "amount": 1000}, false, 0},
		{"termination on end", "terminate", map[string]any{"termination_date": "2026-01-07"}, false, 0},
		{"termination before end", "terminate", map[string]any{"termination_date": "2026-01-06"}, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, handler, token, orgID, propID := propertyTestSetup(t)
			lease := warningLease(t, pool, orgID, propID)
			if tc.nextRate {
				if _, _, err := property.CreateRentChange(context.Background(), pool, orgID, lease.ID, warningDate(t, "2026-01-08"), 60000); err != nil {
					t.Fatal(err)
				}
				warningPeriod(t, pool, orgID, lease, "2026-01-08", "2026-01-14", "open")
			}
			first := warningPeriod(t, pool, orgID, lease, "2026-01-01", "2026-01-07", "open")
			rr := postWarningAction(t, handler, token, lease.ID, tc.action, tc.body)
			if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var response warningActionResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Affected) != tc.want {
				t.Fatalf("affected=%d want %d: %s", len(response.Affected), tc.want, rr.Body.String())
			}
			if tc.want == 1 && response.Affected[0].InvoiceID != first.ID {
				t.Fatal("warned wrong invoice")
			}
			if tc.want == 0 && len(response.Warnings) != 0 {
				t.Fatalf("unexpected warning: %v", response.Warnings)
			}
		})
	}
}
