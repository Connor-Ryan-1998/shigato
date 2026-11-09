//go:build integration

package sandbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/core"
	"shigoto/internal/db"
)

func TestPopulateRefusesExternalExpenseLine(t *testing.T) {
	pool := db.TestPool(t)
	result := populateTest(t, pool)
	other, err := core.CreateOrg(context.Background(), pool, "unrelated", "Keep this organisation")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO expense_lines (org_id,expense_id,category_id,amount,gst_code,gst_amount)
		SELECT $1,e.id,c.id,1000,'FRE',0 FROM expenses e CROSS JOIN categories c
		WHERE e.org_id=$2 AND c.org_id=$1 LIMIT 1`, other.ID, result.Orgs[0].ID)
	before := count(t, pool, `SELECT count(*) FROM expenses`)
	if _, err := Populate(context.Background(), pool, testDay); err == nil {
		t.Fatal("reset accepted a cascading external expense line")
	}
	if got := count(t, pool, `SELECT count(*) FROM expense_lines WHERE org_id=$1`, other.ID); got != 1 {
		t.Fatal("reset deleted an unrelated expense line through ON DELETE CASCADE")
	}
	if got := count(t, pool, `SELECT count(*) FROM expenses`); got != before {
		t.Fatal("reset did not restore owned expenses on refusal")
	}
}

type resetDependencies struct {
	user, file, expense, line, payout int64
}

// Add dependencies a tester can create after population, including payout FKs.
func addResetDependencies(t *testing.T, pool *pgxpool.Pool, orgID int64, email string) resetDependencies {
	t.Helper()
	ctx := context.Background()
	var r resetDependencies
	if err := pool.QueryRow(ctx, `INSERT INTO users (email,name) VALUES ($1,'Reset fixture')
		ON CONFLICT (email) DO UPDATE SET name=users.name RETURNING id`, email).Scan(&r.user); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO memberships (user_id,org_id,role) VALUES ($1,$2,'staff') ON CONFLICT DO NOTHING`, r.user, orgID)
	if err := pool.QueryRow(ctx, `INSERT INTO files (org_id,sha256,mime,size_bytes,original_name)
		VALUES ($1,decode('abcd','hex'),'application/pdf',4,'tester-receipt.pdf') RETURNING id`, orgID).Scan(&r.file); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO expenses (org_id,spent_on,description,receipt_file_id)
		VALUES ($1,$2,'Tester receipt expense',$3) RETURNING id`, orgID, testDay, r.file).Scan(&r.expense); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO expense_lines (org_id,expense_id,category_id,amount,gst_code,gst_amount)
		SELECT $1,$2,id,1000,'FRE',0 FROM categories WHERE org_id=$1 LIMIT 1`, orgID, r.expense)
	var account, imported int64
	if err := pool.QueryRow(ctx, `INSERT INTO bank_accounts (org_id,name,csv_mapping) VALUES ($1,'Reset fixture','{}') RETURNING id`, orgID).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO bank_imports (org_id,bank_account_id,file_sha256,rows_total,rows_new)
		VALUES ($1,$2,decode('abcd','hex'),1,1) RETURNING id`, orgID, account).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO bank_lines (org_id,bank_account_id,import_id,posted_on,amount,description,dedupe_key,status)
		VALUES ($1,$2,$3,$4,9000,'Reset payout',decode('abcd','hex'),'matched') RETURNING id`, orgID, account, imported, testDay).Scan(&r.line); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO payouts (org_id,provider_ref,amount,arrival_on,bank_line_id,fee_expense_id)
		VALUES ($1,'reset-test-payout',9000,$2,$3,$4) RETURNING id`, orgID, testDay, r.line, r.expense).Scan(&r.payout); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `UPDATE payments SET payout_id=$1 WHERE id=(SELECT id FROM payments WHERE org_id=$2 ORDER BY id LIMIT 1)`, r.payout, orgID)
	mustExec(t, pool, `INSERT INTO invites (org_id,email,role,token_hash,invited_by,expires_at)
		VALUES ($1,$2,'staff',decode($3,'hex'),$4,now()+interval '1 day')`, orgID, "pending-"+email, fmt.Sprintf("%016x", orgID), r.user)
	mustExec(t, pool, `INSERT INTO magic_links (token_hash,user_id,expires_at) VALUES (decode($1,'hex'),$2,now()+interval '1 hour')`, fmt.Sprintf("%016x", orgID), r.user)
	mustExec(t, pool, `INSERT INTO sessions (token_hash,user_id,expires_at) VALUES (decode($1,'hex'),$2,now()+interval '1 hour')`, fmt.Sprintf("%016x", orgID), r.user)
	return r
}

func TestPopulateResetsOwnedDependenciesOnly(t *testing.T) {
	pool := db.TestPool(t)
	result := populateTest(t, pool)
	other, err := core.CreateOrg(context.Background(), pool, "unrelated", "Keep this organisation")
	if err != nil {
		t.Fatal(err)
	}
	owned := addResetDependencies(t, pool, result.Orgs[0].ID, "staff@example.test")
	unrelated := addResetDependencies(t, pool, other.ID, "unrelated@example.test")
	populateTest(t, pool)
	for _, row := range []struct {
		table            string
		owned, unrelated int64
	}{
		{"users", owned.user, unrelated.user}, {"files", owned.file, unrelated.file},
		{"expenses", owned.expense, unrelated.expense}, {"bank_lines", owned.line, unrelated.line},
		{"payouts", owned.payout, unrelated.payout},
	} {
		if got := count(t, pool, `SELECT count(*) FROM `+row.table+` WHERE id=$1`, row.owned); got != 0 {
			t.Errorf("owned %s survived", row.table)
		}
		if got := count(t, pool, `SELECT count(*) FROM `+row.table+` WHERE id=$1`, row.unrelated); got != 1 {
			t.Errorf("unrelated %s disappeared", row.table)
		}
	}
	for _, table := range []string{"sessions", "magic_links"} {
		if got := count(t, pool, `SELECT count(*) FROM `+table+` WHERE user_id=$1`, owned.user); got != 0 {
			t.Errorf("owned %s survived", table)
		}
		if got := count(t, pool, `SELECT count(*) FROM `+table+` WHERE user_id=$1`, unrelated.user); got != 1 {
			t.Errorf("unrelated %s disappeared", table)
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM invites`); got != 1 {
		t.Fatal("unrelated invite not preserved or owned invite not removed")
	}
}

func TestPopulateRefusesExternalFileReference(t *testing.T) {
	pool := db.TestPool(t)
	result := populateTest(t, pool)
	owned := addResetDependencies(t, pool, result.Orgs[0].ID, "staff@example.test")
	other, err := core.CreateOrg(context.Background(), pool, "unrelated", "Keep this organisation")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO expenses (org_id,spent_on,description,receipt_file_id) VALUES ($1,$2,'External file reference',$3)`, other.ID, testDay, owned.file)
	before := count(t, pool, `SELECT count(*) FROM expenses`)
	if _, err := Populate(context.Background(), pool, testDay); err == nil {
		t.Fatal("reset accepted external receipt reference")
	}
	if got := count(t, pool, `SELECT count(*) FROM expenses`); got != before {
		t.Fatal("file reference refusal did not restore all expenses")
	}
	if got := count(t, pool, `SELECT count(*) FROM files WHERE id=$1`, owned.file); got != 1 {
		t.Fatal("shared file removed")
	}
}
