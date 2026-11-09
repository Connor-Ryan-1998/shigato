package sandbox

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func reset(ctx context.Context, tx pgx.Tx) error {
	// Lock identities against concurrent membership creation during reset.
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id IN (SELECT user_id FROM sandbox_users) FOR UPDATE`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM orgs WHERE id IN (SELECT org_id FROM sandbox_orgs) FOR UPDATE`); err != nil {
		return err
	}
	// Expense children cascade on parent deletion. Lock parents before checking
	// external children so a concurrent FK insert cannot slip past the check.
	if _, err := tx.Exec(ctx, `SELECT id FROM expenses WHERE org_id IN (SELECT org_id FROM sandbox_orgs) FOR UPDATE`); err != nil {
		return err
	}
	var externalExpenseLine bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM expense_lines WHERE org_id NOT IN (SELECT org_id FROM sandbox_orgs)
		AND expense_id IN (SELECT id FROM expenses WHERE org_id IN (SELECT org_id FROM sandbox_orgs)))`).Scan(&externalExpenseLine); err != nil {
		return err
	}
	if externalExpenseLine {
		return fmt.Errorf("sandbox expense is referenced by an unrelated organisation; remove external references before resetting")
	}
	for _, query := range []string{
		`SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id IN (SELECT user_id FROM sandbox_users) AND org_id NOT IN (SELECT org_id FROM sandbox_orgs))`,
		`SELECT EXISTS (SELECT 1 FROM time_entries WHERE user_id IN (SELECT user_id FROM sandbox_users) AND org_id NOT IN (SELECT org_id FROM sandbox_orgs))`,
		`SELECT EXISTS (SELECT 1 FROM audit_log WHERE actor_user_id IN (SELECT user_id FROM sandbox_users) AND org_id NOT IN (SELECT org_id FROM sandbox_orgs))`,
		`SELECT EXISTS (SELECT 1 FROM invites WHERE invited_by IN (SELECT user_id FROM sandbox_users) AND org_id NOT IN (SELECT org_id FROM sandbox_orgs))`,
	} {
		var shared bool
		if err := tx.QueryRow(ctx, query).Scan(&shared); err != nil {
			return err
		}
		if shared {
			return fmt.Errorf("sandbox identity is referenced outside sandbox organisations; remove external references before resetting")
		}
	}
	for _, table := range []string{"sessions", "magic_links"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+pgx.Identifier{table}.Sanitize()+` WHERE user_id IN (SELECT user_id FROM sandbox_users)`); err != nil {
			return err
		}
	}
	// These three child tables have no org_id. Select ownership via their lease.
	for _, table := range []string{"lease_tenants", "lease_rent_changes", "lease_charges"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+pgx.Identifier{table}.Sanitize()+` WHERE lease_id IN (SELECT id FROM leases WHERE org_id IN (SELECT org_id FROM sandbox_orgs))`); err != nil {
			return err
		}
	}
	// Break lease-to-invoice links before removing invoices. No unrelated row is changed.
	if _, err := tx.Exec(ctx, `UPDATE leases SET bond_invoice_id=NULL, opening_arrears_invoice_id=NULL WHERE org_id IN (SELECT org_id FROM sandbox_orgs)`); err != nil {
		return err
	}
	for _, table := range []string{
		"invites", "audit_log", "webhook_events", "memberships", "time_entries", "rent_periods",
		"payments", "payouts", "expense_lines", "expenses", "invoice_lines", "invoices", "invoice_counters",
		"leases", "project_tasks", "projects", "properties", "bank_lines", "bank_imports", "bank_accounts",
		"files", "categories", "contacts",
	} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+pgx.Identifier{table}.Sanitize()+` WHERE org_id IN (SELECT org_id FROM sandbox_orgs)`); err != nil {
			return fmt.Errorf("cannot reset sandbox %s; check external references: %w", table, err)
		}
	}
	// Registry rows must be removed before their FK targets, without losing IDs.
	if _, err := tx.Exec(ctx, `WITH owned AS (DELETE FROM sandbox_users RETURNING user_id) DELETE FROM users WHERE id IN (SELECT user_id FROM owned)`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `WITH owned AS (DELETE FROM sandbox_orgs RETURNING org_id) DELETE FROM orgs WHERE id IN (SELECT org_id FROM owned)`)
	return err
}
