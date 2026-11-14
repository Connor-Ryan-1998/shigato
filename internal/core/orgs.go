package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned by lookup functions when no matching row exists.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned when the caller is authenticated and the
// resource exists, but the caller isn't allowed to act on it (e.g. editing
// another user's time entry without an admin permission).
var ErrForbidden = errors.New("forbidden")

// Querier covers the subset of pgxpool.Pool and pgx.Tx used by core, so
// callers can pass either a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Org is a tenant organization.
type Org struct {
	ID   int64
	Slug string
	Name string
}

// CreateOrg inserts a new org, seeds its default expense categories, and
// returns it.
func CreateOrg(ctx context.Context, q Querier, slug, name string) (Org, error) {
	var org Org
	err := q.QueryRow(ctx,
		"INSERT INTO orgs (slug, name) VALUES ($1, $2) RETURNING id, slug, name",
		slug, name,
	).Scan(&org.ID, &org.Slug, &org.Name)
	if err != nil {
		return Org{}, fmt.Errorf("creating org %q: %w", slug, err)
	}
	if err := SeedDefaultCategories(ctx, q, org.ID); err != nil {
		return Org{}, fmt.Errorf("seeding default categories for org %q: %w", slug, err)
	}
	return org, nil
}

// defaultExpenseCategory is one row of defaultExpenseCategories.
type defaultExpenseCategory struct {
	Name    string
	GSTCode GSTCode
	Capital bool
	// Item21Label is the rental-schedule label; "" leaves it unmapped.
	Item21Label Item21Label
}

// defaultExpenseCategories lists the 15 default expense categories every
// org gets, editable afterwards. Kept here (rather than in recon, which
// owns the Category type) so CreateOrg can seed them without recon
// importing core, and core importing recon, forming a cycle.
var defaultExpenseCategories = []defaultExpenseCategory{
	{"Repairs & maintenance", GST, false, Item21Repairs},
	{"Insurance", GST, false, Item21Insurance},
	{"Council rates", NT, false, Item21CouncilRates},
	{"Water", FRE, false, Item21Water},
	{"Strata / body corporate", GST, false, Item21BodyCorporate},
	{"Agent & advertising fees", GST, false, Item21AgentFees},
	{"Loan interest", INP, false, Item21Interest},
	{"Bank fees", INP, false, Item21Sundry},
	{"Stripe fees", GST, false, ""},
	{"Software & subscriptions", GST, false, ""},
	{"Accounting & legal", GST, false, Item21Legal},
	{"Travel", GST, false, ""},
	{"Office & supplies", GST, false, ""},
	{"Equipment", GST, true, ""},
	{"Other expenses", GST, false, ""},
}

// SeedDefaultCategories inserts orgID's default expense categories,
// skipping any that already exist by name (ON CONFLICT DO NOTHING), so it's
// safe to call more than once - e.g. against orgs created before this
// migration, from 0009_expenses.sql's own seeding SQL, and again here for
// newly created orgs.
func SeedDefaultCategories(ctx context.Context, q Querier, orgID int64) error {
	for _, c := range defaultExpenseCategories {
		if _, err := q.Exec(ctx, `
			INSERT INTO categories (org_id, name, type, default_gst_code, capital, item21_label)
			VALUES ($1, $2, 'expense', $3, $4, NULLIF($5, ''))
			ON CONFLICT (org_id, name) DO NOTHING`,
			orgID, c.Name, string(c.GSTCode), c.Capital, string(c.Item21Label),
		); err != nil {
			return err
		}
	}
	return nil
}

// OrgBySlug looks up an org by slug. It returns ErrNotFound if no org has
// that slug.
func OrgBySlug(ctx context.Context, q Querier, slug string) (Org, error) {
	var org Org
	err := q.QueryRow(ctx,
		"SELECT id, slug, name FROM orgs WHERE slug = $1",
		slug,
	).Scan(&org.ID, &org.Slug, &org.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Org{}, ErrNotFound
	}
	if err != nil {
		return Org{}, fmt.Errorf("looking up org %q: %w", slug, err)
	}
	return org, nil
}
