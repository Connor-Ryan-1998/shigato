//go:build integration

package property_test

import (
	"time"

	"shigoto/internal/core"
	"shigoto/internal/recon"
	"shigoto/internal/testkit"
)

func ptr[T any](v T) *T { return &v }

// categoryID returns org's seeded category named name.
func categoryID(e *testkit.Env, org core.Org, name string) int64 {
	e.T.Helper()
	var id int64
	if err := e.Pool.QueryRow(e.Ctx, `SELECT id FROM categories WHERE org_id = $1 AND name = $2`, org.ID, name).Scan(&id); err != nil {
		e.T.Fatalf("category %q: %v", name, err)
	}
	return id
}

// expense records a one-line expense of amount (GST inclusive) in
// category on spentOn, tagged to propertyID when non-nil.
func expense(e *testkit.Env, org core.Org, category string, amount core.Cents, spentOn time.Time, propertyID *int64) recon.Expense {
	e.T.Helper()
	exp, _, err := recon.CreateExpense(e.Ctx, e.Pool, org.ID, recon.NewExpense{
		SpentOn: spentOn, Description: category + " bill",
		Lines: []recon.NewExpenseLine{{CategoryID: categoryID(e, org, category), Amount: amount, PropertyID: propertyID}},
	})
	if err != nil {
		e.T.Fatalf("CreateExpense: %v", err)
	}
	return exp
}
