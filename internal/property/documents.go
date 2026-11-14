package property

import (
	"context"

	"shigoto/internal/core"
)

// FileAttachedToProperty reports whether fileID is attached to one of
// propertyID's insurance policies or depreciation items, scoped to orgID.
// Property document downloads serve only attached files, so an upload is
// never readable through a property it doesn't belong to.
func FileAttachedToProperty(ctx context.Context, q core.Querier, orgID, propertyID, fileID int64) (bool, error) {
	var attached bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM insurance_policies
			WHERE org_id = $1 AND property_id = $2 AND file_id = $3
		) OR EXISTS(
			SELECT 1 FROM depreciation_documents dd
			JOIN depreciation_items di ON di.id = dd.item_id
			WHERE di.org_id = $1 AND di.property_id = $2 AND dd.file_id = $3
		)`,
		orgID, propertyID, fileID,
	).Scan(&attached)
	return attached, err
}
