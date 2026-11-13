//go:build integration

package scheduler

import (
	"testing"
	"time"

	"shigoto/internal/testkit"
)

func TestTickRunsRentForAllOrgs(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	orgA := e.Org("acme")
	orgB := e.Org("beta")
	propA := e.Property(orgA).ID
	propB := e.Property(orgB).ID

	asOf := testkit.Date("2026-11-05")
	e.Lease(orgA, propA, e.Contact(orgA, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))
	e.Lease(orgB, propB, e.Contact(orgB, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

	j := Jobs{Pool: pool, Billing: e.BillingService(), Location: time.UTC, PropertyEnabled: true}
	if err := j.Tick(ctx, asOf); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := e.Count("rent_periods", "org_id = $1", orgA.ID); n == 0 {
		t.Errorf("org A rent_periods = %d, want > 0", n)
	}
	if n := e.Count("rent_periods", "org_id = $1", orgB.ID); n == 0 {
		t.Errorf("org B rent_periods = %d, want > 0", n)
	}
}

func TestTickSkipsWhenLocked(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	org := e.Org("acme")
	propID := e.Property(org).ID
	asOf := testkit.Date("2026-11-05")
	e.Lease(org, propID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

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

	j := Jobs{Pool: pool, Billing: e.BillingService(), Location: time.UTC, PropertyEnabled: true}
	if err := j.Tick(ctx, asOf); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := e.Count("rent_periods", "org_id = $1", org.ID); n != 0 {
		t.Errorf("rent_periods = %d, want 0 while locked elsewhere", n)
	}
}

func TestTickPrunesExpiredSessions(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	userID := e.User("user@example.com").ID

	now := testkit.Date("2026-11-05")
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

	j := Jobs{Pool: pool, Billing: e.BillingService(), Location: time.UTC}
	if err := j.Tick(ctx, now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := e.Count("sessions", ""); n != 1 {
		t.Fatalf("sessions remaining = %d, want 1 (the active one)", n)
	}
}

func TestTickPropertyDisabled(t *testing.T) {
	e := testkit.New(t)
	pool, ctx := e.Pool, e.Ctx

	org := e.Org("acme")
	propID := e.Property(org).ID
	asOf := testkit.Date("2026-11-05")
	e.Lease(org, propID, e.Contact(org, testkit.Kind("tenant"), testkit.Name("Test Tenant")).ID, testkit.Start(asOf))

	j := Jobs{Pool: pool, Billing: e.BillingService(), Location: time.UTC, PropertyEnabled: false}
	if err := j.Tick(ctx, asOf); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := e.Count("rent_periods", "org_id = $1", org.ID); n != 0 {
		t.Errorf("rent_periods = %d, want 0 when PropertyEnabled is false", n)
	}
}
