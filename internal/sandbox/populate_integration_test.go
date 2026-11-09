//go:build integration

package sandbox

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/db"
)

var testDay = time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func populateTest(t *testing.T, pool *pgxpool.Pool) Result {
	t.Helper()
	r, err := Populate(context.Background(), pool, testDay)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPopulateCollision(t *testing.T) {
	for _, kind := range []string{"slug", "email"} {
		t.Run(kind, func(t *testing.T) {
			pool := db.TestPool(t)
			if kind == "slug" {
				if _, err := core.CreateOrg(context.Background(), pool, "sbx-property", "Existing work"); err != nil {
					t.Fatal(err)
				}
			} else {
				mustExec(t, pool, `INSERT INTO users (email, name) VALUES ('OWNER@EXAMPLE.TEST', 'Existing person')`)
			}
			_, err := Populate(context.Background(), pool, testDay)
			if err == nil || !strings.Contains(err.Error(), "collision") {
				t.Fatalf("expected collision refusal, got %v", err)
			}
			if n := count(t, pool, `SELECT count(*) FROM sandbox_orgs`); n != 0 {
				t.Fatalf("partially populated ownership: %d", n)
			}
			if n := count(t, pool, `SELECT count(*) FROM users WHERE name='Existing person'`); kind == "email" && n != 1 {
				t.Fatal("existing user changed")
			}
		})
	}
}

func TestPopulatePreservesUnrelatedData(t *testing.T) {
	pool := db.TestPool(t)
	org, err := core.CreateOrg(context.Background(), pool, "existing", "Existing work")
	if err != nil {
		t.Fatal(err)
	}
	contact, err := core.CreateContact(context.Background(), pool, org.ID, core.Contact{Kind: "client", Name: "Keep me"})
	if err != nil {
		t.Fatal(err)
	}
	first := populateTest(t, pool)
	second := populateTest(t, pool)
	if len(first.Accounts) == 0 || len(second.Orgs) != 2 {
		t.Fatalf("missing account or organisation output: %+v", second)
	}
	if n := count(t, pool, `SELECT count(*) FROM contacts WHERE id=$1 AND name='Keep me'`, contact.ID); n != 1 {
		t.Fatal("unrelated contact changed")
	}
	if n := count(t, pool, `SELECT count(*) FROM orgs`); n != 3 {
		t.Fatalf("organisation count = %d, want 3", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM sandbox_users`); n != len(second.Accounts) {
		t.Fatalf("duplicate users: %d", n)
	}
}

func TestPopulateRefusesSharedUser(t *testing.T) {
	pool := db.TestPool(t)
	populateTest(t, pool)
	org, err := core.CreateOrg(context.Background(), pool, "existing", "Existing work")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO memberships (user_id, org_id, role) SELECT id, $1, 'staff' FROM users WHERE email='owner@example.test'`, org.ID)
	before := count(t, pool, `SELECT count(*) FROM contacts`)
	if _, err := Populate(context.Background(), pool, testDay); err == nil {
		t.Fatal("reset accepted a shared identity")
	}
	if n := count(t, pool, `SELECT count(*) FROM memberships WHERE org_id=$1`, org.ID); n != 1 {
		t.Fatal("unrelated membership removed")
	}
	if n := count(t, pool, `SELECT count(*) FROM contacts`); n != before {
		t.Fatal("refusal changed sandbox data")
	}
}

func TestPopulateRollback(t *testing.T) {
	pool := db.TestPool(t)
	populateTest(t, pool)
	mustExec(t, pool, `UPDATE orgs SET name='Tester edit' WHERE slug='sbx-property'`)
	mustExec(t, pool, `CREATE FUNCTION fail_sandbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected population failure'; END $$`)
	mustExec(t, pool, `CREATE TRIGGER fail_sandbox BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION fail_sandbox()`)
	if _, err := Populate(context.Background(), pool, testDay); err == nil {
		t.Fatal("expected injected failure")
	}
	if n := count(t, pool, `SELECT count(*) FROM orgs WHERE name='Tester edit'`); n != 1 {
		t.Fatal("previous sandbox was not restored")
	}
}

func TestPopulateConcurrent(t *testing.T) {
	pool := db.TestPool(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Populate(context.Background(), pool, testDay)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM orgs`); n != 2 {
		t.Fatalf("concurrent run left %d organisations", n)
	}
}
