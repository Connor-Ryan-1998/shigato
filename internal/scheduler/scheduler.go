// Package scheduler runs shigoto's recurring in-process jobs: hourly rent
// runs and session pruning. Because property.RunRent is idempotent, an
// hourly tick is cheap and catches up after downtime.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/property"
)

// lockID is the Postgres advisory lock key held for the duration of one
// Tick, so that when multiple instances run the scheduler, only one of
// them does the work for a given tick.
const lockID = 7414

// Jobs holds the scheduler's dependencies.
type Jobs struct {
	Pool    *pgxpool.Pool
	Billing *billing.Service
	// Location is used to turn now into the calendar date RunRent treats
	// as "today". Nil means UTC.
	Location *time.Location
	// PropertyEnabled gates the rent run; it should match
	// Config.Modules["property"].
	PropertyEnabled bool
}

// Run calls s.Tick(ctx, now()) once immediately, then again every tick of
// every, until ctx is done. A failing Tick is logged, not fatal: the
// scheduler keeps ticking.
func Run(ctx context.Context, s Jobs, every time.Duration, now func() time.Time) {
	tick := func() {
		if err := s.Tick(ctx, now()); err != nil {
			slog.Error("scheduler tick failed", "error", err)
		}
	}

	tick()

	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// Tick runs one pass of every job, guarded by a Postgres advisory lock so
// only one running instance does the work for a given tick: if the lock is
// already held elsewhere, Tick does nothing and returns nil. An error for
// one org's rent run is logged with that org's id and slug; the remaining
// orgs still run. Tick itself returns an error only when a step shared by
// every org fails: acquiring the lock connection or listing orgs.
func (j Jobs) Tick(ctx context.Context, now time.Time) error {
	conn, err := j.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked); err != nil {
		return fmt.Errorf("acquiring scheduler lock: %w", err)
	}
	if !locked {
		return nil
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID); err != nil {
			slog.Error("releasing scheduler lock", "error", err)
		}
	}()

	if j.PropertyEnabled {
		orgs, err := listOrgs(ctx, j.Pool)
		if err != nil {
			return fmt.Errorf("listing orgs: %w", err)
		}

		asOf := asOfDate(now, j.Location)
		for _, org := range orgs {
			if _, err := property.RunRent(ctx, j.Billing, org.id, org.slug, asOf); err != nil {
				slog.Error("rent run failed for org", "org_id", org.id, "org_slug", org.slug, "error", err)
			}
		}
	}

	if err := auth.PruneSessions(ctx, j.Pool, now); err != nil {
		slog.Error("pruning sessions failed", "error", err)
	}

	return nil
}

// asOfDate turns now into the calendar date RunRent treats as "today": now
// in loc (UTC if loc is nil), re-anchored at UTC midnight.
func asOfDate(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// org is the bit of core.Org the scheduler needs to run rent for one.
type org struct {
	id   int64
	slug string
}

// listOrgs returns every org, in id order.
func listOrgs(ctx context.Context, pool *pgxpool.Pool) ([]org, error) {
	rows, err := pool.Query(ctx, "SELECT id, slug FROM orgs ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []org
	for rows.Next() {
		var o org
		if err := rows.Scan(&o.id, &o.slug); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}
