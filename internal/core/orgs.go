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

// CreateOrg inserts a new org and returns it.
func CreateOrg(ctx context.Context, q Querier, slug, name string) (Org, error) {
	var org Org
	err := q.QueryRow(ctx,
		"INSERT INTO orgs (slug, name) VALUES ($1, $2) RETURNING id, slug, name",
		slug, name,
	).Scan(&org.ID, &org.Slug, &org.Name)
	if err != nil {
		return Org{}, fmt.Errorf("creating org %q: %w", slug, err)
	}
	return org, nil
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
