package timesheets

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/core"
)

// Entry is one logged block of time against a Task. InvoiceLineID is set
// once the entry has been billed; a billed entry is locked (Update/Delete
// give core.ErrInvalidState).
type Entry struct {
	ID            int64
	OrgID         int64
	TaskID        int64
	UserID        int64
	WorkDate      time.Time
	Minutes       int
	Note          string
	InvoiceLineID *int64
}

const entryColumns = `id, org_id, task_id, user_id, work_date, minutes, note, invoice_line_id`

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	err := row.Scan(&e.ID, &e.OrgID, &e.TaskID, &e.UserID, &e.WorkDate, &e.Minutes, &e.Note, &e.InvoiceLineID)
	return e, err
}

// validateEntryFields checks the fields Create/UpdateEntry share: minutes,
// note length and that work_date isn't in the future. today is the
// caller's current calendar date (UTC midnight) in Config.Location.
func validateEntryFields(e Entry, today time.Time) error {
	if e.Minutes < 1 || e.Minutes > 1440 {
		return fmt.Errorf("%w: minutes must be between 1 and 1440", core.ErrInvalidInput)
	}
	if utf8.RuneCountInString(e.Note) > 1000 {
		return fmt.Errorf("%w: note must be at most 1000 characters", core.ErrInvalidInput)
	}
	if e.WorkDate.After(today) {
		return fmt.Errorf("%w: work_date must not be after today", core.ErrInvalidInput)
	}
	return nil
}

// checkTaskForEntry looks up taskID in orgID and requires both the task and
// its project to be active. Any failure - cross-org task, inactive task,
// inactive project - is ErrInvalidInput (422): from CreateEntry/UpdateEntry's
// point of view the task simply isn't a valid place to log time, whether or
// not it exists elsewhere.
func checkTaskForEntry(ctx context.Context, q core.Querier, orgID, taskID int64) error {
	task, err := GetTask(ctx, q, orgID, taskID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("%w: task_id not found", core.ErrInvalidInput)
		}
		return err
	}
	if !task.Active {
		return fmt.Errorf("%w: task is not active", core.ErrInvalidInput)
	}
	project, err := GetProject(ctx, q, orgID, task.ProjectID)
	if err != nil {
		return err
	}
	if !project.Active {
		return fmt.Errorf("%w: project is not active", core.ErrInvalidInput)
	}
	return nil
}

// CreateEntry validates e and inserts it for orgID/userID against e.TaskID.
// The task must exist in orgID, and both the task and its project must be
// active, otherwise core.ErrInvalidInput. today is the caller's current
// calendar date (UTC midnight) in Config.Location; e.WorkDate must not be
// after it.
func CreateEntry(ctx context.Context, q core.Querier, orgID, userID int64, e Entry, today time.Time) (Entry, error) {
	if err := validateEntryFields(e, today); err != nil {
		return Entry{}, err
	}
	if err := checkTaskForEntry(ctx, q, orgID, e.TaskID); err != nil {
		return Entry{}, err
	}

	row := q.QueryRow(ctx, `
		INSERT INTO time_entries (org_id, task_id, user_id, work_date, minutes, note)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+entryColumns,
		orgID, e.TaskID, userID, e.WorkDate, e.Minutes, e.Note,
	)
	return scanEntry(row)
}

// lockEntry locks and returns the entry row (entries table only, no join),
// scoped to orgID. It returns core.ErrNotFound if no such entry exists in
// that org.
func lockEntry(ctx context.Context, q core.Querier, orgID, id int64) (Entry, error) {
	row := q.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM time_entries WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		orgID, id,
	)
	e, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, core.ErrNotFound
	}
	return e, err
}

// checkOwnership applies the controller's ruling on the order of checks
// once an entry row is locked: not found (another org) already happened in
// lockEntry; next, billed beats forbidden, so a billed entry the actor
// doesn't own still reports core.ErrInvalidState, not core.ErrForbidden.
func checkOwnership(e Entry, actor auth.Principal) error {
	if e.InvoiceLineID != nil {
		return core.ErrInvalidState
	}
	if e.UserID != actor.UserID && !auth.Can(actor.Role, auth.PermTimesheetsAdmin) {
		return core.ErrForbidden
	}
	return nil
}

// UpdateEntry locks entry id in orgID (SELECT ... FOR UPDATE), then
// validates e and replaces its task_id, work_date, minutes and note. The
// entry's user_id never changes. A billed entry gives core.ErrInvalidState
// (409). An entry the actor doesn't own gives core.ErrForbidden (403),
// unless the actor's role holds auth.PermTimesheetsAdmin. Moving the entry
// to e.TaskID (task_id actually changing) requires the same task rules as
// CreateEntry; leaving it unchanged does not re-check the task/project, so
// editing minutes or the note on an unbilled entry still works once its
// project has since been deactivated. today is the caller's current
// calendar date, as in CreateEntry.
func UpdateEntry(ctx context.Context, q core.Querier, orgID int64, actor auth.Principal, id int64, e Entry, today time.Time) (Entry, error) {
	existing, err := lockEntry(ctx, q, orgID, id)
	if err != nil {
		return Entry{}, err
	}
	if err := checkOwnership(existing, actor); err != nil {
		return Entry{}, err
	}
	if err := validateEntryFields(e, today); err != nil {
		return Entry{}, err
	}
	if e.TaskID != existing.TaskID {
		if err := checkTaskForEntry(ctx, q, orgID, e.TaskID); err != nil {
			return Entry{}, err
		}
	}

	row := q.QueryRow(ctx, `
		UPDATE time_entries SET task_id = $1, work_date = $2, minutes = $3, note = $4
		WHERE org_id = $5 AND id = $6
		RETURNING `+entryColumns,
		e.TaskID, e.WorkDate, e.Minutes, e.Note, orgID, id,
	)
	return scanEntry(row)
}

// DeleteEntry locks entry id in orgID and deletes it, subject to the same
// billed/ownership rules as UpdateEntry.
func DeleteEntry(ctx context.Context, q core.Querier, orgID int64, actor auth.Principal, id int64) error {
	existing, err := lockEntry(ctx, q, orgID, id)
	if err != nil {
		return err
	}
	if err := checkOwnership(existing, actor); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `DELETE FROM time_entries WHERE org_id = $1 AND id = $2`, orgID, id)
	return err
}

// EntryFilter narrows ListEntries. From/To are inclusive date bounds.
// ProjectID and ClientID filter through the task's project (ClientID via
// the project's client contact). Unbilled, when true, restricts to entries
// with no invoice_line_id; Billed to entries with one.
type EntryFilter struct {
	From, To          *time.Time
	TaskID, ProjectID *int64
	UserID, ClientID  *int64
	Unbilled, Billed  bool
}

// EntryDetail is an Entry plus the names and rate a timesheet list shows
// beside it, so a client doesn't need one lookup per task, project,
// customer and user.
type EntryDetail struct {
	Entry
	TaskName        string
	ProjectID       int64
	ProjectName     string
	BillingMethod   string
	ClientContactID int64
	ClientName      string
	UserName        string
	// Rate is the task's effective hourly rate (its override, else the
	// project's).
	Rate core.Cents
}

// ListEntries lists orgID's time entries matching filter, with their
// details, ordered by work_date then id.
func ListEntries(ctx context.Context, q core.Querier, orgID int64, filter EntryFilter) ([]EntryDetail, error) {
	sql := `SELECT e.id, e.org_id, e.task_id, e.user_id, e.work_date, e.minutes, e.note, e.invoice_line_id,
		t.name, p.id, p.name, p.billing_method, c.id, c.name, u.name, COALESCE(t.hourly_rate, p.hourly_rate)
		FROM time_entries e
		JOIN project_tasks t ON t.id = e.task_id
		JOIN projects p ON p.id = t.project_id
		JOIN contacts c ON c.id = p.client_contact_id
		JOIN users u ON u.id = e.user_id
		WHERE e.org_id = $1`
	args := []any{orgID}

	add := func(clause string, val any) {
		args = append(args, val)
		sql += fmt.Sprintf(" AND %s $%d", clause, len(args))
	}
	if filter.From != nil {
		add("e.work_date >=", *filter.From)
	}
	if filter.To != nil {
		add("e.work_date <=", *filter.To)
	}
	if filter.TaskID != nil {
		add("e.task_id =", *filter.TaskID)
	}
	if filter.UserID != nil {
		add("e.user_id =", *filter.UserID)
	}
	if filter.ProjectID != nil {
		add("p.id =", *filter.ProjectID)
	}
	if filter.ClientID != nil {
		add("p.client_contact_id =", *filter.ClientID)
	}
	if filter.Unbilled {
		sql += " AND e.invoice_line_id IS NULL"
	}
	if filter.Billed {
		sql += " AND e.invoice_line_id IS NOT NULL"
	}
	sql += " ORDER BY e.work_date, e.id"

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []EntryDetail{}
	for rows.Next() {
		var d EntryDetail
		e := &d.Entry
		if err := rows.Scan(&e.ID, &e.OrgID, &e.TaskID, &e.UserID, &e.WorkDate, &e.Minutes, &e.Note, &e.InvoiceLineID,
			&d.TaskName, &d.ProjectID, &d.ProjectName, &d.BillingMethod, &d.ClientContactID, &d.ClientName, &d.UserName, &d.Rate,
		); err != nil {
			return nil, err
		}
		entries = append(entries, d)
	}
	return entries, rows.Err()
}
