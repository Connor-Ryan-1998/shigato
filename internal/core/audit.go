package core

import (
	"context"
	"encoding/json"
)

// Audit records one row in audit_log. actor is nil when the write wasn't
// made on behalf of a logged-in user (e.g. a future webhook-driven write).
// diff is marshaled to JSON; a nil diff stores a SQL NULL.
func Audit(ctx context.Context, q Querier, orgID int64, actor *int64, entity string, id int64, action string, diff any) error {
	var diffJSON []byte
	if diff != nil {
		b, err := json.Marshal(diff)
		if err != nil {
			return err
		}
		diffJSON = b
	}
	_, err := q.Exec(ctx,
		`INSERT INTO audit_log (org_id, actor_user_id, entity, entity_id, action, diff)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		orgID, actor, entity, id, action, diffJSON,
	)
	return err
}
