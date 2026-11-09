// Package sandbox builds disposable local development data.
package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/core"
)

// Account describes how to access one committed sandbox identity.
// Password is empty for portal users, who must use normal magic-link login.
type Account struct {
	Email    string
	Role     string
	OrgSlug  string
	Password string
}

// Result contains access details, returned only after the transaction commits.
type Result struct {
	Orgs     []core.Org
	Accounts []Account
}

var orgDefinitions = []struct{ slug, name string }{
	{"sbx-property", "Sandbox Property Management"},
	{"sbx-consulting", "Sandbox Consulting"},
}

var accountDefinitions = []struct {
	name, role string
	org        int
}{
	{"owner", "owner", 0},
	{"staff", "staff", 0},
	{"bookkeeper", "bookkeeper", 0},
	{"tenant", "tenant", 0},
	{"tenant-other", "tenant", 0},
	{"tenant-empty", "tenant", 0},
	{"client", "client", 0},
	{"client-empty", "client", 0},
	{"tenant-beta", "tenant", 1},
	{"client-beta", "client", 1},
}

// Populate replaces explicitly owned sandbox records in the configured database.
// All writes, including replacement, commit together; external references abort
// the reset instead of deleting unrelated data. It never contacts providers.
func Populate(ctx context.Context, pool *pgxpool.Pool, today time.Time) (Result, error) {
	if today.IsZero() {
		return Result{}, fmt.Errorf("population date is required")
	}
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // no-op after commit
	// Transaction-scoped: another invocation waits, then sees the committed registry.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(736449, 1)`); err != nil {
		return Result{}, err
	}
	result, err := populate(ctx, tx, today)
	if err != nil {
		return Result{}, fmt.Errorf("populate sandbox (no changes committed): %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func populate(ctx context.Context, tx pgx.Tx, today time.Time) (Result, error) {
	if err := checkCollisions(ctx, tx); err != nil {
		return Result{}, err
	}
	if err := reset(ctx, tx); err != nil {
		return Result{}, err
	}
	var result Result
	users := make(map[string]int64)
	for _, def := range orgDefinitions {
		org, err := core.CreateOrg(ctx, tx, def.slug, def.name)
		if err != nil {
			return Result{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sandbox_orgs (org_id) VALUES ($1)`, org.ID); err != nil {
			return Result{}, err
		}
		result.Orgs = append(result.Orgs, org)
	}
	for _, def := range accountDefinitions {
		org := result.Orgs[def.org]
		account := Account{Email: def.name + "@example.test", Role: def.role, OrgSlug: org.Slug}
		var hash *string
		if def.role != "tenant" && def.role != "client" {
			secret := make([]byte, 18)
			if _, err := rand.Read(secret); err != nil {
				return Result{}, err
			}
			account.Password = base64.RawURLEncoding.EncodeToString(secret)
			h, err := auth.HashPassword(account.Password)
			if err != nil {
				return Result{}, err
			}
			hash = &h
		}
		var userID int64
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ($1,$2,$3) RETURNING id`,
			account.Email, "Sandbox "+def.name, hash).Scan(&userID); err != nil {
			return Result{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sandbox_users (user_id) VALUES ($1)`, userID); err != nil {
			return Result{}, err
		}
		var contactID *int64
		if def.role == "tenant" || def.role == "client" {
			c, err := core.CreateContact(ctx, tx, org.ID, core.Contact{Kind: def.role, Name: "Sandbox " + def.name, Email: account.Email})
			if err != nil {
				return Result{}, err
			}
			contactID = &c.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role, contact_id) VALUES ($1,$2,$3,$4)`,
			userID, org.ID, def.role, contactID); err != nil {
			return Result{}, err
		}
		if def.role == "owner" {
			if _, err := tx.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1,$2,'owner')`, userID, result.Orgs[1].ID); err != nil {
				return Result{}, err
			}
		}
		result.Accounts = append(result.Accounts, account)
		users[def.name] = userID
	}
	if err := seedScenarios(ctx, tx, today, result.Orgs, users); err != nil {
		return Result{}, err
	}
	return result, nil
}

func checkCollisions(ctx context.Context, tx pgx.Tx) error {
	for _, def := range orgDefinitions {
		var collision bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM orgs o WHERE slug=$1 AND NOT EXISTS (SELECT 1 FROM sandbox_orgs s WHERE s.org_id=o.id))`, def.slug).Scan(&collision); err != nil {
			return err
		}
		if collision {
			return fmt.Errorf("organisation collision: %s is not owned by populate-sbx", def.slug)
		}
	}
	for _, def := range accountDefinitions {
		var collision bool
		email := def.name + "@example.test"
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM users u WHERE email=$1 AND NOT EXISTS (SELECT 1 FROM sandbox_users s WHERE s.user_id=u.id))`, email).Scan(&collision); err != nil {
			return err
		}
		if collision {
			return fmt.Errorf("account collision: %s is not owned by populate-sbx", email)
		}
	}
	return nil
}
