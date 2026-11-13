//go:build integration

package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPool returns a connection pool scoped to a fresh, migrated schema for
// use in integration tests. It skips the test if SHIGOTO_TEST_DATABASE_URL
// is unset. The schema is dropped in t.Cleanup.
func TestPool(t testing.TB) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("SHIGOTO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SHIGOTO_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("opening admin pool: %v", err)
	}

	schema := "t_" + randomSuffix(t)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("creating schema %s: %v", schema, err)
	}

	// Registered first so it runs last: the pool below must be closed
	// before its schema is dropped.
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("dropping schema %s: %v", schema, err)
		}
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parsing test database url: %v", err)
	}
	// public must follow the test schema so citext (installed once into
	// public by the migration) still resolves.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("opening test pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrating test schema: %v", err)
	}

	return pool
}

func randomSuffix(t testing.TB) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generating random schema suffix: %v", err)
	}
	return hex.EncodeToString(buf)
}
