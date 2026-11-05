//go:build integration

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/payments"
	"shigoto/internal/property"
)

func noProvider(string) (payments.Provider, error) {
	return nil, payments.ErrNoProvider
}

func testService(pool *pgxpool.Pool) *billing.Service {
	return billing.New(pool, func(string) string { return "" }, noProvider, time.UTC)
}

func testOrg(t *testing.T, pool *pgxpool.Pool, slug string) core.Org {
	t.Helper()
	org, err := core.CreateOrg(context.Background(), pool, slug, slug+" org")
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", slug, err)
	}
	return org
}

func testContact(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	c, err := core.CreateContact(context.Background(), pool, orgID, core.Contact{Kind: "tenant", Name: "Test Tenant"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	return c.ID
}

func testProperty(t *testing.T, pool *pgxpool.Pool, orgID int64) int64 {
	t.Helper()
	p, err := property.CreateProperty(context.Background(), pool, orgID, property.Property{Name: "1 Test St", Residential: true})
	if err != nil {
		t.Fatalf("CreateProperty: %v", err)
	}
	return p.ID
}

// testWeeklyLease creates a weekly lease with one primary tenant, starting
// on start.
func testWeeklyLease(t *testing.T, pool *pgxpool.Pool, orgID, propertyID int64, start time.Time) property.Lease {
	t.Helper()
	tenantID := testContact(t, pool, orgID)
	lease, err := property.CreateLease(context.Background(), pool, orgID, property.NewLease{
		PropertyID: propertyID, StartDate: start, Frequency: property.Weekly, RentAmount: 50000,
		Tenants: []property.NewLeaseTenant{{ContactID: tenantID, Primary: true}},
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	return lease
}

func rentPeriodCount(t *testing.T, pool *pgxpool.Pool, orgID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM rent_periods WHERE org_id = $1", orgID,
	).Scan(&n); err != nil {
		t.Fatalf("counting rent_periods: %v", err)
	}
	return n
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %s: %v", s, err)
	}
	return d
}

func TestTickRunsRentForAllOrgs(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	orgA := testOrg(t, pool, "acme")
	orgB := testOrg(t, pool, "beta")
	propA := testProperty(t, pool, orgA.ID)
	propB := testProperty(t, pool, orgB.ID)

	asOf := mustDate(t, "2026-11-05")
	testWeeklyLease(t, pool, orgA.ID, propA, asOf)
	testWeeklyLease(t, pool, orgB.ID, propB, asOf)

	j := Jobs{Pool: pool, Billing: testService(pool), Location: time.UTC, PropertyEnabled: true}
	if err := j.Tick(ctx, asOf); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := rentPeriodCount(t, pool, orgA.ID); n == 0 {
		t.Errorf("org A rent_periods = %d, want > 0", n)
	}
	if n := rentPeriodCount(t, pool, orgB.ID); n == 0 {
		t.Errorf("org B rent_periods = %d, want > 0", n)
	}
}

func TestTickSkipsWhenLocked(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	org := testOrg(t, pool, "acme")
	propID := testProperty(t, pool, org.ID)
	asOf := mustDate(t, "2026-11-05")
	testWeeklyLease(t, pool, org.ID, propID, asOf)

	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring lock connection: %v", err)
	}
	defer lockConn.Release()
	var locked bool
	if err := lockConn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked); err != nil {
		t.Fatalf("taking lock: %v", err)
	}
	if !locked {
		t.Fatal("expected to take the lock")
	}
	defer lockConn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)

	j := Jobs{Pool: pool, Billing: testService(pool), Location: time.UTC, PropertyEnabled: true}
	if err := j.Tick(ctx, asOf); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := rentPeriodCount(t, pool, org.ID); n != 0 {
		t.Errorf("rent_periods = %d, want 0 while locked elsewhere", n)
	}
}

func TestTickPrunesExpiredSessions(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	var userID int64
	if err := pool.QueryRow(ctx,
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		"user@example.com", "User", "hash",
	).Scan(&userID); err != nil {
		t.Fatalf("creating user: %v", err)
	}

	now := mustDate(t, "2026-11-05")
	if _, err := pool.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
		[]byte("expired"), userID, now.Add(-time.Hour),
	); err != nil {
		t.Fatalf("creating expired session: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
		[]byte("active"), userID, now.Add(time.Hour),
	); err != nil {
		t.Fatalf("creating active session: %v", err)
	}

	j := Jobs{Pool: pool, Billing: testService(pool), Location: time.UTC}
	if err := j.Tick(ctx, now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("counting sessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("sessions remaining = %d, want 1 (the active one)", n)
	}
}

func TestTickPropertyDisabled(t *testing.T) {
	pool := db.TestPool(t)
	ctx := context.Background()

	org := testOrg(t, pool, "acme")
	propID := testProperty(t, pool, org.ID)
	asOf := mustDate(t, "2026-11-05")
	testWeeklyLease(t, pool, org.ID, propID, asOf)

	j := Jobs{Pool: pool, Billing: testService(pool), Location: time.UTC, PropertyEnabled: false}
	if err := j.Tick(ctx, asOf); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := rentPeriodCount(t, pool, org.ID); n != 0 {
		t.Errorf("rent_periods = %d, want 0 when PropertyEnabled is false", n)
	}
}
