//go:build integration

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/db"
)

// insertInviteRow inserts an invites row directly with a fixed expires_at
// and used_at, bypassing CreateInvite/AcceptInvite, so
// TestPruneInvitesRetainsForThirtyDaysPastExpiry can place rows at exact
// points in the retention window without waiting real time.
func insertInviteRow(t *testing.T, pool *pgxpool.Pool, orgID int64, email string, expiresAt time.Time, usedAt *time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO invites (org_id, email, role, token_hash, expires_at, used_at)
		 VALUES ($1, $2, 'staff', $3, $4, $5)`,
		orgID, email, hashToken(email), expiresAt, usedAt,
	); err != nil {
		t.Fatalf("insertInviteRow(%s): %v", email, err)
	}
}

func inviteExists(t *testing.T, pool *pgxpool.Pool, email string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		"SELECT true FROM invites WHERE email = $1", email,
	).Scan(&exists); err != nil {
		return false
	}
	return exists
}

// TestPruneInvitesRetainsForThirtyDaysPastExpiry covers the fix for an
// expired/used invite link showing "not found" instead of "expired":
// PruneInvites must keep a used or expired row around for inviteRetention
// (30 days) past its expiry, not delete it within the hour.
func TestPruneInvitesRetainsForThirtyDaysPastExpiry(t *testing.T) {
	pool := db.TestPool(t)
	org := createTestOrg(t, pool, "acme")
	now := time.Now()
	used := now.Add(-20 * 24 * time.Hour)

	insertInviteRow(t, pool, org.ID, "live@example.com", now.Add(time.Hour), nil)
	insertInviteRow(t, pool, org.ID, "recently-expired@example.com", now.Add(-time.Hour), nil)
	insertInviteRow(t, pool, org.ID, "recently-used@example.com", now.Add(time.Hour), &used)
	insertInviteRow(t, pool, org.ID, "long-expired@example.com", now.Add(-31*24*time.Hour), nil)

	if err := PruneInvites(context.Background(), pool, now); err != nil {
		t.Fatalf("PruneInvites: %v", err)
	}

	if !inviteExists(t, pool, "live@example.com") {
		t.Errorf("an unused, unexpired invite was pruned")
	}
	if !inviteExists(t, pool, "recently-expired@example.com") {
		t.Errorf("an invite expired less than 30 days ago was pruned - it should still show \"expired\", not \"not found\"")
	}
	if !inviteExists(t, pool, "recently-used@example.com") {
		t.Errorf("a used invite whose expiry is still within the retention window was pruned")
	}
	if inviteExists(t, pool, "long-expired@example.com") {
		t.Errorf("an invite expired more than 30 days ago was kept")
	}
}
