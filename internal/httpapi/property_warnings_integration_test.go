//go:build integration

package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
)

// warningLeaseTerms are the leases' pinned terms: weekly from 2026-01-01 at
// 50000 cents. The rent periods and affected-invoice dates below follow
// from them.
var warningLeaseTerms = []testkit.LeaseOption{
	testkit.Start(testkit.Date("2026-01-01")), testkit.Frequency(property.Weekly), testkit.Rent(50000),
}

// warningTables hold every financial record a lease action must not rewrite.
var warningTables = []string{"invoices", "invoice_lines", "payments", "rent_periods"}

func warningPeriod(e *testkit.Env, org core.Org, lease property.Lease, start, end, status string) core.Invoice {
	t := e.T
	t.Helper()
	ctx := e.Ctx
	orgID := org.ID
	tx, err := e.Pool.Begin(ctx)
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
		inv, err = core.IssueInvoice(ctx, tx, orgID, inv.ID, testkit.Date(start))
		if err != nil {
			t.Fatal(err)
		}
		if status == "paid" {
			_, _, err = core.RecordPayment(ctx, tx, orgID, core.NewPayment{InvoiceID: inv.ID, Amount: inv.Total, PaidOn: testkit.Date(start), Method: "bank_transfer"})
		} else if status != "open" {
			_, err = tx.Exec(ctx, "UPDATE invoices SET status=$1 WHERE id=$2 AND org_id=$3", status, inv.ID, orgID)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO rent_periods(org_id,lease_id,period_start,period_end,amount_due,invoice_id) VALUES($1,$2,$3,$4,$5,$6)", orgID, lease.ID, testkit.Date(start), testkit.Date(end), inv.Total, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return inv
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
			e := testkit.New(t)
			handler := e.Handler(testkit.OnlyModules("property"))
			org, owner := e.OwnerSession("acme", handler)
			prop := e.Property(org, testkit.PropertyName("1 Test St"), testkit.Residential())
			lease := e.Lease(org, prop.ID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Warning tenant")).ID, warningLeaseTerms...)
			statuses := []string{"draft", "open", "paid", "uncollectible", "void"}
			var invoiceIDs []int64
			var numbers []*int
			for i, status := range statuses {
				start := testkit.Date("2026-01-01").AddDate(0, 0, 7*i)
				inv := warningPeriod(e, org, lease, start.Format("2006-01-02"), start.AddDate(0, 0, 6).Format("2006-01-02"), status)
				if status != "void" {
					invoiceIDs = append(invoiceIDs, inv.ID)
					numbers = append(numbers, inv.Number)
				}
			}
			otherLease := e.Lease(org, prop.ID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Warning tenant")).ID, warningLeaseTerms...)
			warningPeriod(e, org, otherLease, "2026-01-01", "2026-01-07", "open")
			otherOrg := e.Org("foreign")
			foreignProp := e.Property(otherOrg, testkit.PropertyName("1 Test St"), testkit.Residential())
			foreignLease := e.Lease(otherOrg, foreignProp.ID, e.Contact(otherOrg, testkit.Kind("tenant"), testkit.Name("Warning tenant")).ID, warningLeaseTerms...)
			warningPeriod(e, otherOrg, foreignLease, "2026-01-01", "2026-01-07", "open")
			if _, err := e.Pool.Exec(e.Ctx, "INSERT INTO rent_periods(org_id,lease_id,period_start,period_end,amount_due) VALUES($1,$2,'2026-02-05','2026-02-11',50000)", org.ID, lease.ID); err != nil {
				t.Fatal(err)
			}
			if action == "rent-changes" {
				if _, _, err := property.CreateRentChange(e.Ctx, e.Pool, org.ID, lease.ID, testkit.Date("2026-01-02"), 52000); err != nil {
					t.Fatal(err)
				}
			}
			before := e.Snapshot(warningTables...)
			var body any
			switch action {
			case "rent-changes":
				body = map[string]any{"effective_date": "2026-01-03", "amount": 55000}
			case "charges":
				body = map[string]any{"description": "Water", "start_date": "2026-01-02", "amount": 1000}
			case "terminate":
				body = map[string]any{"termination_date": "2026-01-02"}
			}
			rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/leases/%d/%s", lease.ID, action), body)
			wantStatus := http.StatusCreated
			if action == "terminate" {
				wantStatus = http.StatusOK
			}
			if rr.Code != wantStatus {
				t.Fatalf("status %d: %s", rr.Code, rr.Body)
			}
			var response warningActionResponse
			if err := json.Unmarshal(rr.Body, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Warnings) == 0 || len(response.Affected) != 4 {
				t.Fatalf("missing billed-period warning: %s", rr.Body)
			}
			if action == "rent-changes" && len(response.Warnings) != 2 {
				t.Fatalf("rent increase warning lost: %v", response.Warnings)
			}
			for i, affected := range response.Affected {
				if affected.InvoiceID != invoiceIDs[i] || affected.Status != statuses[i] || !reflect.DeepEqual(affected.Number, numbers[i]) {
					t.Fatalf("bad affected invoice: %+v", affected)
				}
				wantStart := testkit.Date("2026-01-01").AddDate(0, 0, 7*i)
				if affected.Start != wantStart.Format("2006-01-02") || affected.End != wantStart.AddDate(0, 0, 6).Format("2006-01-02") {
					t.Fatalf("bad period: %+v", affected)
				}
			}
			if response.ID == 0 {
				t.Fatalf("lost existing flattened response: %s", rr.Body)
			}
			if got := e.Snapshot(warningTables...); got != before {
				t.Fatal("lease action rewrote invoice, line, payment or rent period")
			}
			updated, err := property.GetLease(e.Ctx, e.Pool, org.ID, lease.ID)
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
			denied := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/leases/%d/%s", foreignLease.ID, action), body)
			if denied.Code != http.StatusNotFound {
				t.Fatalf("foreign lease: status %d", denied.Code)
			}
		})
	}
}

func TestPropertyWarningQueryFailureRollsBackMutation(t *testing.T) {
	e := testkit.New(t)
	handler := e.Handler(testkit.OnlyModules("property"))
	org, owner := e.OwnerSession("acme", handler)
	prop := e.Property(org, testkit.PropertyName("1 Test St"), testkit.Residential())
	lease := e.Lease(org, prop.ID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Warning tenant")).ID, warningLeaseTerms...)
	ctx := e.Ctx
	pool := e.Pool
	if _, err := pool.Exec(ctx, "ALTER TABLE rent_periods RENAME TO saved_rent_periods"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, "ALTER TABLE saved_rent_periods RENAME TO rent_periods"); err != nil {
			t.Error(err)
		}
	}()
	before := e.Count("audit_log", "")
	for _, action := range []struct {
		name string
		body any
	}{
		{"rent-changes", map[string]any{"effective_date": "2026-01-02", "amount": 55000}},
		{"charges", map[string]any{"description": "Water", "start_date": "2026-01-02", "amount": 1000}},
		{"terminate", map[string]any{"termination_date": "2026-01-02"}},
	} {
		rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/leases/%d/%s", lease.ID, action.name), action.body)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("%s status %d want 500: %s", action.name, rr.Code, rr.Body)
		}
	}
	updated, err := property.GetLease(ctx, pool, org.ID, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RentChanges) != 1 || len(updated.Charges) != 0 || updated.TerminationDate != nil {
		t.Fatalf("failed warning query persisted mutation: %+v", updated)
	}
	after := e.Count("audit_log", "")
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
			e := testkit.New(t)
			handler := e.Handler(testkit.OnlyModules("property"))
			org, owner := e.OwnerSession("acme", handler)
			prop := e.Property(org, testkit.PropertyName("1 Test St"), testkit.Residential())
			lease := e.Lease(org, prop.ID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Warning tenant")).ID, warningLeaseTerms...)
			if tc.nextRate {
				if _, _, err := property.CreateRentChange(e.Ctx, e.Pool, org.ID, lease.ID, testkit.Date("2026-01-08"), 60000); err != nil {
					t.Fatal(err)
				}
				warningPeriod(e, org, lease, "2026-01-08", "2026-01-14", "open")
			}
			first := warningPeriod(e, org, lease, "2026-01-01", "2026-01-07", "open")
			rr := owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/leases/%d/%s", lease.ID, tc.action), tc.body)
			if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body)
			}
			var response warningActionResponse
			if err := json.Unmarshal(rr.Body, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Affected) != tc.want {
				t.Fatalf("affected=%d want %d: %s", len(response.Affected), tc.want, rr.Body)
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
