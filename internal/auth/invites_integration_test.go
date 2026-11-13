//go:build integration

package auth_test

import (
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/testkit"
)

// insertInviteRow inserts an invites row directly with a fixed expires_at
// and used_at, bypassing CreateInvite/AcceptInvite (and testkit's Invite,
// which only offers live or just-expired rows), so
// TestPruneInvitesRetainsForThirtyDaysPastExpiry can place rows at exact
// points in the retention window without waiting real time.
func insertInviteRow(e *testkit.Env, org core.Org, email string, expiresAt time.Time, usedAt *time.Time) {
	e.T.Helper()
	if _, err := e.Pool.Exec(e.Ctx,
		`INSERT INTO invites (org_id, email, role, token_hash, expires_at, used_at)
		 VALUES ($1, $2, 'staff', $3, $4, $5)`,
		org.ID, email, auth.HashToken(email), expiresAt, usedAt,
	); err != nil {
		e.T.Fatalf("insertInviteRow(%s): %v", email, err)
	}
}

// TestPruneInvitesRetainsForThirtyDaysPastExpiry covers the fix for an
// expired/used invite link showing "not found" instead of "expired":
// PruneInvites must keep a used or expired row around for inviteRetention
// (30 days) past its expiry, not delete it within the hour.
func TestPruneInvitesRetainsForThirtyDaysPastExpiry(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	now := time.Now()
	used := now.Add(-20 * 24 * time.Hour)

	insertInviteRow(e, org, "live@example.com", now.Add(time.Hour), nil)
	insertInviteRow(e, org, "recently-expired@example.com", now.Add(-time.Hour), nil)
	insertInviteRow(e, org, "recently-used@example.com", now.Add(time.Hour), &used)
	insertInviteRow(e, org, "long-expired@example.com", now.Add(-31*24*time.Hour), nil)

	if err := auth.PruneInvites(e.Ctx, e.Pool, now); err != nil {
		t.Fatalf("PruneInvites: %v", err)
	}

	inviteExists := func(email string) bool { return e.Count("invites", "email = $1", email) > 0 }
	if !inviteExists("live@example.com") {
		t.Errorf("an unused, unexpired invite was pruned")
	}
	if !inviteExists("recently-expired@example.com") {
		t.Errorf("an invite expired less than 30 days ago was pruned - it should still show \"expired\", not \"not found\"")
	}
	if !inviteExists("recently-used@example.com") {
		t.Errorf("a used invite whose expiry is still within the retention window was pruned")
	}
	if inviteExists("long-expired@example.com") {
		t.Errorf("an invite expired more than 30 days ago was kept")
	}
}
