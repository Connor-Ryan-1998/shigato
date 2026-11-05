package recon

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
)

// Category classifies an income or expense line, carrying a default GST
// code the line uses unless overridden. An org's 15 default expense
// categories are seeded by core.CreateOrg (internal/core/orgs.go); from
// there they're editable like any other category.
type Category struct {
	ID             int64
	OrgID          int64
	Name           string
	Type           string // "income" or "expense"
	DefaultGSTCode core.GSTCode
	Capital        bool
}

const categoryColumns = `id, org_id, name, type, coalesce(default_gst_code, ''), capital`

func scanCategory(row pgx.Row) (Category, error) {
	var c Category
	var gst string
	if err := row.Scan(&c.ID, &c.OrgID, &c.Name, &c.Type, &gst, &c.Capital); err != nil {
		return Category{}, err
	}
	c.DefaultGSTCode = core.GSTCode(gst)
	return c, nil
}

// validateCategory checks name, typ and gst against the category rules
// shared by create and the strict-PUT update. default_gst_code is required
// and must be a valid core.GSTCode: an expense line whose category has no
// default (and no explicit override) can't be given a GST treatment, so
// CreateExpense would otherwise have to reject it at expense-creation time
// instead of here. (Categories seeded, or updated, before this rule existed
// may still have an empty default_gst_code; CreateExpense rejects a line
// that resolves to one.)
func validateCategory(name, typ string, gst core.GSTCode) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	switch typ {
	case "income", "expense":
	default:
		return fmt.Errorf("%w: type must be income or expense", core.ErrInvalidInput)
	}
	if _, err := core.ParseGSTCode(string(gst)); err != nil {
		return fmt.Errorf("%w: default_gst_code: %v", core.ErrInvalidInput, err)
	}
	return nil
}

// CreateCategory inserts a new category for orgID. A duplicate name in the
// org gives ErrInvalidInput, checked before insert and backstopped against
// the unique_violation a concurrent request could race into (the same
// pattern as CreateBankAccount).
func CreateCategory(ctx context.Context, q core.Querier, orgID int64, name, typ string, defaultGST core.GSTCode, capital bool) (Category, error) {
	if err := validateCategory(name, typ, defaultGST); err != nil {
		return Category{}, err
	}

	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM categories WHERE org_id = $1 AND name = $2)`, orgID, name).Scan(&exists); err != nil {
		return Category{}, err
	}
	if exists {
		return Category{}, fmt.Errorf("%w: a category named %q already exists", core.ErrInvalidInput, name)
	}

	row := q.QueryRow(ctx, `
		INSERT INTO categories (org_id, name, type, default_gst_code, capital)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		RETURNING `+categoryColumns,
		orgID, name, typ, string(defaultGST), capital,
	)
	created, err := scanCategory(row)
	if isUniqueViolation(err) {
		return Category{}, fmt.Errorf("%w: a category named %q already exists", core.ErrInvalidInput, name)
	}
	return created, err
}

// ListCategories lists orgID's categories.
func ListCategories(ctx context.Context, q core.Querier, orgID int64) ([]Category, error) {
	rows, err := q.Query(ctx, `SELECT `+categoryColumns+` FROM categories WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	categories := []Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

// GetCategory looks up a category by id, scoped to orgID. It returns
// core.ErrNotFound if no such category exists in that org - including when
// the id belongs to another org.
func GetCategory(ctx context.Context, q core.Querier, orgID, id int64) (Category, error) {
	row := q.QueryRow(ctx, `SELECT `+categoryColumns+` FROM categories WHERE org_id = $1 AND id = $2`, orgID, id)
	c, err := scanCategory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Category{}, core.ErrNotFound
	}
	return c, err
}

// UpdateCategory replaces orgID's category id wholesale (strict PUT: every
// field is given on every call). It returns core.ErrNotFound if no such
// category exists in that org.
func UpdateCategory(ctx context.Context, q core.Querier, orgID, id int64, name, typ string, defaultGST core.GSTCode, capital bool) (Category, error) {
	if err := validateCategory(name, typ, defaultGST); err != nil {
		return Category{}, err
	}

	row := q.QueryRow(ctx, `
		UPDATE categories SET name = $1, type = $2, default_gst_code = NULLIF($3, ''), capital = $4
		WHERE org_id = $5 AND id = $6
		RETURNING `+categoryColumns,
		name, typ, string(defaultGST), capital, orgID, id,
	)
	updated, err := scanCategory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Category{}, core.ErrNotFound
	}
	if isUniqueViolation(err) {
		return Category{}, fmt.Errorf("%w: a category named %q already exists", core.ErrInvalidInput, name)
	}
	return updated, err
}
