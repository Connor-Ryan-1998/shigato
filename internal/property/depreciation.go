package property

import (
	"context"
	"fmt"
	"time"

	"shigoto/internal/core"
)

// DepreciationItem is one line of a property's depreciation schedule: the
// building (capital works) or an asset (plant and equipment), with its
// yearly deductions as the surveyor's report, or the owner, states them.
// Shigoto records the amounts; it never calculates them.
type DepreciationItem struct {
	ID          int64
	PropertyID  int64
	Name        string
	Type        string // capital_works or plant_equipment
	InstalledOn time.Time
	Cost        *core.Cents
	Notes       string
	// ExpenseID links the expense that paid for the item, if recorded.
	ExpenseID *int64
	Amounts   []DepreciationAmount
	// Documents are the supporting files. Create and Update read only
	// each File's ID.
	Documents []core.File
}

// DepreciationAmount is one financial year's deduction for an item. FY is
// the financial year's end year (FY2027 = 2027).
type DepreciationAmount struct {
	FY     int
	Amount core.Cents
}

// AmountFor returns item's deduction for financial year fy (0 if none).
func (i DepreciationItem) AmountFor(fy int) core.Cents {
	for _, a := range i.Amounts {
		if a.FY == fy {
			return a.Amount
		}
	}
	return 0
}

var depreciationTypes = map[string]bool{"capital_works": true, "plant_equipment": true}

func validateDepreciation(ctx context.Context, q core.Querier, orgID int64, in DepreciationItem) error {
	if in.Name == "" {
		return fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	if !depreciationTypes[in.Type] {
		return fmt.Errorf("%w: type must be capital_works or plant_equipment", core.ErrInvalidInput)
	}
	if in.InstalledOn.IsZero() {
		return fmt.Errorf("%w: installed_on is required", core.ErrInvalidInput)
	}
	if in.Cost != nil && *in.Cost < 0 {
		return fmt.Errorf("%w: cost must not be negative", core.ErrInvalidInput)
	}
	seen := map[int]bool{}
	for _, a := range in.Amounts {
		if a.FY < 1900 || a.FY > 2200 {
			return fmt.Errorf("%w: invalid financial year %d", core.ErrInvalidInput, a.FY)
		}
		if seen[a.FY] {
			return fmt.Errorf("%w: FY%d appears more than once", core.ErrInvalidInput, a.FY)
		}
		seen[a.FY] = true
		if a.Amount <= 0 {
			return fmt.Errorf("%w: FY%d amount must be positive", core.ErrInvalidInput, a.FY)
		}
	}
	if in.ExpenseID != nil {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM expenses WHERE org_id = $1 AND id = $2)`, orgID, *in.ExpenseID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: expense %d not found", core.ErrInvalidInput, *in.ExpenseID)
		}
	}
	for _, d := range in.Documents {
		if err := requireOrgFile(ctx, q, orgID, d.ID); err != nil {
			return err
		}
	}
	return nil
}

// replaceDepreciationChildren rewrites itemID's amounts and documents.
func replaceDepreciationChildren(ctx context.Context, q core.Querier, itemID int64, in DepreciationItem) error {
	if _, err := q.Exec(ctx, `DELETE FROM depreciation_amounts WHERE item_id = $1`, itemID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM depreciation_documents WHERE item_id = $1`, itemID); err != nil {
		return err
	}
	for _, a := range in.Amounts {
		if _, err := q.Exec(ctx, `INSERT INTO depreciation_amounts (item_id, fy, amount) VALUES ($1, $2, $3)`, itemID, a.FY, int64(a.Amount)); err != nil {
			return err
		}
	}
	for _, d := range in.Documents {
		if _, err := q.Exec(ctx, `INSERT INTO depreciation_documents (item_id, file_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, itemID, d.ID); err != nil {
			return err
		}
	}
	return nil
}

// CreateDepreciationItem adds in to its property, scoped to orgID.
func CreateDepreciationItem(ctx context.Context, q core.Querier, orgID int64, in DepreciationItem) (DepreciationItem, error) {
	if err := requireProperty(ctx, q, orgID, in.PropertyID); err != nil {
		return DepreciationItem{}, err
	}
	if err := validateDepreciation(ctx, q, orgID, in); err != nil {
		return DepreciationItem{}, err
	}
	var id int64
	if err := q.QueryRow(ctx, `
		INSERT INTO depreciation_items (org_id, property_id, name, type, installed_on, cost, notes, expense_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		orgID, in.PropertyID, in.Name, in.Type, in.InstalledOn, int64Ptr(in.Cost), in.Notes, in.ExpenseID,
	).Scan(&id); err != nil {
		return DepreciationItem{}, err
	}
	if err := replaceDepreciationChildren(ctx, q, id, in); err != nil {
		return DepreciationItem{}, err
	}
	return getDepreciationItem(ctx, q, orgID, in.PropertyID, id)
}

// UpdateDepreciationItem replaces item in.ID on in.PropertyID, including
// its amounts and documents, scoped to orgID.
func UpdateDepreciationItem(ctx context.Context, q core.Querier, orgID int64, in DepreciationItem) (DepreciationItem, error) {
	if _, err := getDepreciationItem(ctx, q, orgID, in.PropertyID, in.ID); err != nil {
		return DepreciationItem{}, err
	}
	if err := validateDepreciation(ctx, q, orgID, in); err != nil {
		return DepreciationItem{}, err
	}
	if _, err := q.Exec(ctx, `
		UPDATE depreciation_items SET name = $4, type = $5, installed_on = $6, cost = $7, notes = $8, expense_id = $9
		WHERE org_id = $1 AND property_id = $2 AND id = $3`,
		orgID, in.PropertyID, in.ID, in.Name, in.Type, in.InstalledOn, int64Ptr(in.Cost), in.Notes, in.ExpenseID,
	); err != nil {
		return DepreciationItem{}, err
	}
	if err := replaceDepreciationChildren(ctx, q, in.ID, in); err != nil {
		return DepreciationItem{}, err
	}
	return getDepreciationItem(ctx, q, orgID, in.PropertyID, in.ID)
}

// DeleteDepreciationItem removes item id from propertyID, scoped to orgID.
func DeleteDepreciationItem(ctx context.Context, q core.Querier, orgID, propertyID, id int64) error {
	tag, err := q.Exec(ctx, `DELETE FROM depreciation_items WHERE org_id = $1 AND property_id = $2 AND id = $3`, orgID, propertyID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return core.ErrNotFound
	}
	return nil
}

func getDepreciationItem(ctx context.Context, q core.Querier, orgID, propertyID, id int64) (DepreciationItem, error) {
	items, err := listDepreciation(ctx, q, orgID, &propertyID, &id)
	if err != nil {
		return DepreciationItem{}, err
	}
	if len(items) == 0 {
		return DepreciationItem{}, core.ErrNotFound
	}
	return items[0], nil
}

// ListDepreciationItems lists propertyID's items by install date. It
// returns core.ErrNotFound if the property is not orgID's.
func ListDepreciationItems(ctx context.Context, q core.Querier, orgID, propertyID int64) ([]DepreciationItem, error) {
	if err := requireProperty(ctx, q, orgID, propertyID); err != nil {
		return nil, err
	}
	return listDepreciation(ctx, q, orgID, &propertyID, nil)
}

// listDepreciation loads orgID's items, optionally narrowed to one
// property and/or one item, with their amounts and documents.
func listDepreciation(ctx context.Context, q core.Querier, orgID int64, propertyID, itemID *int64) ([]DepreciationItem, error) {
	rows, err := q.Query(ctx, `
		SELECT id, property_id, name, type, installed_on, cost, notes, expense_id
		FROM depreciation_items
		WHERE org_id = $1 AND ($2::bigint IS NULL OR property_id = $2) AND ($3::bigint IS NULL OR id = $3)
		ORDER BY installed_on, id`,
		orgID, propertyID, itemID,
	)
	if err != nil {
		return nil, err
	}
	items := []DepreciationItem{}
	index := map[int64]int{}
	for rows.Next() {
		var it DepreciationItem
		var cost *int64
		if err := rows.Scan(&it.ID, &it.PropertyID, &it.Name, &it.Type, &it.InstalledOn, &cost, &it.Notes, &it.ExpenseID); err != nil {
			rows.Close()
			return nil, err
		}
		it.Cost = centsPtr(cost)
		it.Amounts = []DepreciationAmount{}
		it.Documents = []core.File{}
		index[it.ID] = len(items)
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(items) == 0 {
		return items, err
	}
	ids := make([]int64, 0, len(items))
	for id := range index {
		ids = append(ids, id)
	}

	amounts, err := q.Query(ctx, `SELECT item_id, fy, amount FROM depreciation_amounts WHERE item_id = ANY($1) ORDER BY fy`, ids)
	if err != nil {
		return nil, err
	}
	for amounts.Next() {
		var id int64
		var a DepreciationAmount
		var amount int64
		if err := amounts.Scan(&id, &a.FY, &amount); err != nil {
			amounts.Close()
			return nil, err
		}
		a.Amount = core.Cents(amount)
		items[index[id]].Amounts = append(items[index[id]].Amounts, a)
	}
	amounts.Close()
	if err := amounts.Err(); err != nil {
		return nil, err
	}

	docs, err := q.Query(ctx, `
		SELECT dd.item_id, f.id, f.org_id, f.sha256, f.mime, f.size_bytes, f.original_name, f.created_at
		FROM depreciation_documents dd JOIN files f ON f.id = dd.file_id
		WHERE dd.item_id = ANY($1) ORDER BY f.id`, ids)
	if err != nil {
		return nil, err
	}
	defer docs.Close()
	for docs.Next() {
		var id int64
		var f core.File
		if err := docs.Scan(&id, &f.ID, &f.OrgID, &f.SHA256, &f.MIME, &f.SizeBytes, &f.OriginalName, &f.CreatedAt); err != nil {
			return nil, err
		}
		items[index[id]].Documents = append(items[index[id]].Documents, f)
	}
	if err := docs.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
