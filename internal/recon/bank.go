package recon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/importers"
)

// isUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505), following the same pre-check-plus-backstop pattern as
// internal/timesheets.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const bankAccountColumns = `id, org_id, name, csv_mapping, created_at`

func scanBankAccount(row pgx.Row) (BankAccount, error) {
	var a BankAccount
	var mapping []byte
	if err := row.Scan(&a.ID, &a.OrgID, &a.Name, &mapping, &a.CreatedAt); err != nil {
		return BankAccount{}, err
	}
	if err := json.Unmarshal(mapping, &a.CSVMapping); err != nil {
		return BankAccount{}, err
	}
	return a, nil
}

// CreateBankAccount validates name and m, then inserts a bank account for
// orgID. A duplicate name in the org gives ErrInvalidInput, checked before
// insert and backstopped against the unique_violation a concurrent request
// could race into.
func CreateBankAccount(ctx context.Context, q core.Querier, orgID int64, name string, m importers.CSVMapping) (BankAccount, error) {
	if name == "" {
		return BankAccount{}, fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	if err := m.Validate(); err != nil {
		return BankAccount{}, err
	}
	mapping, err := json.Marshal(m)
	if err != nil {
		return BankAccount{}, err
	}

	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bank_accounts WHERE org_id = $1 AND name = $2)`, orgID, name).Scan(&exists); err != nil {
		return BankAccount{}, err
	}
	if exists {
		return BankAccount{}, fmt.Errorf("%w: a bank account named %q already exists", core.ErrInvalidInput, name)
	}

	row := q.QueryRow(ctx, `
		INSERT INTO bank_accounts (org_id, name, csv_mapping)
		VALUES ($1, $2, $3)
		RETURNING `+bankAccountColumns,
		orgID, name, mapping,
	)
	created, err := scanBankAccount(row)
	if isUniqueViolation(err) {
		return BankAccount{}, fmt.Errorf("%w: a bank account named %q already exists", core.ErrInvalidInput, name)
	}
	return created, err
}

// ListBankAccounts lists orgID's bank accounts.
func ListBankAccounts(ctx context.Context, q core.Querier, orgID int64) ([]BankAccount, error) {
	rows, err := q.Query(ctx, `SELECT `+bankAccountColumns+` FROM bank_accounts WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []BankAccount{}
	for rows.Next() {
		a, err := scanBankAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// GetBankAccount looks up a bank account by id, scoped to orgID. It
// returns core.ErrNotFound if no such account exists in that org -
// including when the id belongs to another org, so a cross-org id can't be
// distinguished from an unknown one.
func GetBankAccount(ctx context.Context, q core.Querier, orgID, id int64) (BankAccount, error) {
	row := q.QueryRow(ctx, `SELECT `+bankAccountColumns+` FROM bank_accounts WHERE org_id = $1 AND id = $2`, orgID, id)
	a, err := scanBankAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return BankAccount{}, core.ErrNotFound
	}
	return a, err
}

// Import parses file against accountID's CSV mapping and inserts its lines
// for orgID, in one transaction. Lines whose dedupe key already exists for
// this bank account are silently skipped (ON CONFLICT DO NOTHING), so
// importing the same file - or an overlapping later export - twice only
// ever adds genuinely new rows; the unique constraint, not an
// application-level check, is what makes this correct under concurrent
// imports of the same file. The bank_imports row and an audit entry are
// recorded in the same transaction, with billing.ActorFrom(ctx) as the
// actor.
func Import(ctx context.Context, pool *pgxpool.Pool, orgID, accountID int64, file io.Reader) (ImportResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	account, err := GetBankAccount(ctx, tx, orgID, accountID)
	if err != nil {
		return ImportResult{}, err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, file); err != nil {
		return ImportResult{}, err
	}
	sum := sha256.Sum256(buf.Bytes())

	lines, err := importers.NewCSV(account.CSVMapping).Lines(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return ImportResult{}, err
	}
	keys := importers.DedupeKeys(lines)

	// bank_imports is inserted first (with placeholder row counts) so its
	// id exists for bank_lines.import_id's NOT NULL FK; the real counts
	// are filled in once the line inserts below are done.
	var importID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO bank_imports (org_id, bank_account_id, file_sha256, rows_total, rows_new)
		VALUES ($1, $2, $3, 0, 0)
		RETURNING id`,
		orgID, accountID, sum[:],
	).Scan(&importID); err != nil {
		return ImportResult{}, err
	}

	result := ImportResult{RowsTotal: len(lines)}
	for i, l := range lines {
		tag, err := tx.Exec(ctx, `
			INSERT INTO bank_lines (org_id, bank_account_id, import_id, posted_on, amount, description, dedupe_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (bank_account_id, dedupe_key) DO NOTHING`,
			orgID, accountID, importID, l.PostedOn, int64(l.Amount), l.Description, keys[i],
		)
		if err != nil {
			return ImportResult{}, err
		}
		if tag.RowsAffected() > 0 {
			result.RowsNew++
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE bank_imports SET rows_total = $1, rows_new = $2 WHERE id = $3`,
		result.RowsTotal, result.RowsNew, importID,
	); err != nil {
		return ImportResult{}, err
	}

	if err := core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_import", importID, "create", map[string]any{
		"bank_account_id": accountID, "rows_total": result.RowsTotal, "rows_new": result.RowsNew,
	}); err != nil {
		return ImportResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

const bankLineColumns = `id, org_id, bank_account_id, import_id, posted_on, amount, description, status, ignore_reason`

func scanBankLine(row pgx.Row) (BankLine, error) {
	var l BankLine
	var amount int64
	err := row.Scan(&l.ID, &l.OrgID, &l.BankAccountID, &l.ImportID, &l.PostedOn, &amount, &l.Description, &l.Status, &l.IgnoreReason)
	l.Amount = core.Cents(amount)
	return l, err
}

// ListBankLines lists orgID's bank lines matching filter.
func ListBankLines(ctx context.Context, q core.Querier, orgID int64, filter BankLineFilter) ([]BankLine, error) {
	sql := `SELECT ` + bankLineColumns + ` FROM bank_lines WHERE org_id = $1`
	args := []any{orgID}
	if filter.BankAccountID != nil {
		args = append(args, *filter.BankAccountID)
		sql += fmt.Sprintf(" AND bank_account_id = $%d", len(args))
	}
	if filter.Status != nil {
		args = append(args, *filter.Status)
		sql += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if filter.From != nil {
		args = append(args, *filter.From)
		sql += fmt.Sprintf(" AND posted_on >= $%d", len(args))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		sql += fmt.Sprintf(" AND posted_on <= $%d", len(args))
	}
	sql += " ORDER BY posted_on, id"

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lines := []BankLine{}
	for rows.Next() {
		l, err := scanBankLine(rows)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}
