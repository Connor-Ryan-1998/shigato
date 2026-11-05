//go:build integration

package db

import (
	"context"
	"testing"
)

func TestMigrateIdempotent(t *testing.T) {
	pool := TestPool(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate call failed: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("counting schema_migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("schema_migrations count = %d, want 1", count)
	}
}

func TestCoreConstraints(t *testing.T) {
	pool := TestPool(t)
	ctx := context.Background()

	var orgID, contactID, invoiceID int64
	if err := pool.QueryRow(ctx,
		"INSERT INTO orgs (slug, name) VALUES ('acme', 'Acme') RETURNING id",
	).Scan(&orgID); err != nil {
		t.Fatalf("inserting org: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"INSERT INTO contacts (org_id, kind, name) VALUES ($1, 'client', 'Bob') RETURNING id",
		orgID,
	).Scan(&contactID); err != nil {
		t.Fatalf("inserting contact: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"INSERT INTO invoices (org_id, contact_id, number) VALUES ($1, $2, 1) RETURNING id",
		orgID, contactID,
	).Scan(&invoiceID); err != nil {
		t.Fatalf("inserting invoice: %v", err)
	}

	if _, err := pool.Exec(ctx,
		"INSERT INTO payments (org_id, invoice_id, amount, paid_on, method) VALUES ($1, $2, 0, now(), 'cash')",
		orgID, invoiceID,
	); err == nil {
		t.Fatal("expected error inserting payment with amount = 0, got nil")
	}

	if _, err := pool.Exec(ctx,
		"INSERT INTO invoices (org_id, contact_id, number) VALUES ($1, $2, 1)",
		orgID, contactID,
	); err == nil {
		t.Fatal("expected error inserting duplicate (org_id, number) invoice, got nil")
	}
}
