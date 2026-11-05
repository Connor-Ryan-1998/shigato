package timesheets

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"shigoto/internal/core"
)

// isUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505). Used as a defensive, concurrency-safe backstop behind a
// pre-insert check for the same condition (two concurrent requests racing
// to create a project or task with the same name).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// validateGSTCode checks code is one a project may hold: GST or FRE (the
// two codes consulting lines use; see the GST-on-consulting-lines rule).
func validateGSTCode(code core.GSTCode) error {
	switch code {
	case core.GST, core.FRE:
		return nil
	default:
		return fmt.Errorf("%w: gst_code must be GST or FRE", core.ErrInvalidInput)
	}
}

// --- projects ---

const projectColumns = `id, org_id, client_contact_id, name, hourly_rate, gst_code, active`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	var gstCode string
	err := row.Scan(&p.ID, &p.OrgID, &p.ClientContactID, &p.Name, &p.HourlyRate, &gstCode, &p.Active)
	p.GSTCode = core.GSTCode(gstCode)
	return p, err
}

// validateProject checks the fields CreateProject/UpdateProject share:
// name, rate and GST code. The client contact is validated separately by
// CreateProject (UpdateProject never changes it).
func validateProject(p Project) error {
	if p.Name == "" {
		return fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	if p.HourlyRate <= 0 {
		return fmt.Errorf("%w: hourly_rate must be positive", core.ErrInvalidInput)
	}
	return validateGSTCode(p.GSTCode)
}

// CreateProject validates p and inserts it for orgID. p.ClientContactID
// must be a contact of kind "client" in orgID, otherwise ErrInvalidInput. A
// duplicate name in the org also gives ErrInvalidInput, checked before
// insert and backstopped against the unique_violation a concurrent request
// could race into. p.Active is ignored: a new project is always active.
func CreateProject(ctx context.Context, q core.Querier, orgID int64, p Project) (Project, error) {
	if err := validateProject(p); err != nil {
		return Project{}, err
	}
	contact, err := core.GetContact(ctx, q, orgID, p.ClientContactID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return Project{}, fmt.Errorf("%w: client_contact_id not found", core.ErrInvalidInput)
		}
		return Project{}, err
	}
	if contact.Kind != "client" {
		return Project{}, fmt.Errorf("%w: client_contact_id is not a client", core.ErrInvalidInput)
	}

	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE org_id = $1 AND name = $2)`, orgID, p.Name).Scan(&exists); err != nil {
		return Project{}, err
	}
	if exists {
		return Project{}, fmt.Errorf("%w: a project named %q already exists", core.ErrInvalidInput, p.Name)
	}

	row := q.QueryRow(ctx, `
		INSERT INTO projects (org_id, client_contact_id, name, hourly_rate, gst_code, active)
		VALUES ($1, $2, $3, $4, $5, true)
		RETURNING `+projectColumns,
		orgID, p.ClientContactID, p.Name, int64(p.HourlyRate), string(p.GSTCode),
	)
	created, err := scanProject(row)
	if isUniqueViolation(err) {
		return Project{}, fmt.Errorf("%w: a project named %q already exists", core.ErrInvalidInput, p.Name)
	}
	return created, err
}

// GetProject looks up a project by id, scoped to orgID. It returns
// core.ErrNotFound if no such project exists in that org.
func GetProject(ctx context.Context, q core.Querier, orgID, id int64) (Project, error) {
	row := q.QueryRow(ctx, `SELECT `+projectColumns+` FROM projects WHERE org_id = $1 AND id = $2`, orgID, id)
	p, err := scanProject(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, core.ErrNotFound
	}
	return p, err
}

// ListProjects lists an org's projects, optionally filtered by client
// contact and/or restricted to active ones.
func ListProjects(ctx context.Context, q core.Querier, orgID int64, clientID *int64, activeOnly bool) ([]Project, error) {
	sql := `SELECT ` + projectColumns + ` FROM projects WHERE org_id = $1`
	args := []any{orgID}
	if clientID != nil {
		args = append(args, *clientID)
		sql += fmt.Sprintf(" AND client_contact_id = $%d", len(args))
	}
	if activeOnly {
		sql += " AND active"
	}
	sql += " ORDER BY id"

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// UpdateProject validates p and updates name, hourly_rate, gst_code and
// active for the project id in orgID. Its client contact cannot be
// changed. It returns core.ErrNotFound if no such project exists in that
// org, or ErrInvalidInput for a duplicate name among the org's other
// projects.
func UpdateProject(ctx context.Context, q core.Querier, orgID, id int64, p Project) (Project, error) {
	if err := validateProject(p); err != nil {
		return Project{}, err
	}
	if _, err := GetProject(ctx, q, orgID, id); err != nil {
		return Project{}, err
	}

	var exists bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM projects WHERE org_id = $1 AND name = $2 AND id != $3)`,
		orgID, p.Name, id,
	).Scan(&exists); err != nil {
		return Project{}, err
	}
	if exists {
		return Project{}, fmt.Errorf("%w: a project named %q already exists", core.ErrInvalidInput, p.Name)
	}

	row := q.QueryRow(ctx, `
		UPDATE projects SET name = $1, hourly_rate = $2, gst_code = $3, active = $4
		WHERE org_id = $5 AND id = $6
		RETURNING `+projectColumns,
		p.Name, int64(p.HourlyRate), string(p.GSTCode), p.Active, orgID, id,
	)
	updated, err := scanProject(row)
	if isUniqueViolation(err) {
		return Project{}, fmt.Errorf("%w: a project named %q already exists", core.ErrInvalidInput, p.Name)
	}
	return updated, err
}

// --- tasks ---

const taskColumns = `id, org_id, project_id, name, hourly_rate, active`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	var rate *int64
	err := row.Scan(&t.ID, &t.OrgID, &t.ProjectID, &t.Name, &rate, &t.Active)
	if rate != nil {
		c := core.Cents(*rate)
		t.HourlyRate = &c
	}
	return t, err
}

// validateTask checks the fields CreateTask/UpdateTask share: name and rate
// override.
func validateTask(t Task) error {
	if t.Name == "" {
		return fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	if t.HourlyRate != nil && *t.HourlyRate <= 0 {
		return fmt.Errorf("%w: hourly_rate must be positive", core.ErrInvalidInput)
	}
	return nil
}

// CreateTask validates t and inserts it under projectID in orgID. projectID
// must exist in orgID, otherwise core.ErrNotFound (404). A duplicate name
// within the project gives ErrInvalidInput (422), checked before insert and
// backstopped against the unique_violation a concurrent request could race
// into.
func CreateTask(ctx context.Context, q core.Querier, orgID, projectID int64, t Task) (Task, error) {
	if err := validateTask(t); err != nil {
		return Task{}, err
	}
	if _, err := GetProject(ctx, q, orgID, projectID); err != nil {
		return Task{}, err
	}

	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_tasks WHERE project_id = $1 AND name = $2)`, projectID, t.Name).Scan(&exists); err != nil {
		return Task{}, err
	}
	if exists {
		return Task{}, fmt.Errorf("%w: a task named %q already exists in this project", core.ErrInvalidInput, t.Name)
	}

	var rate *int64
	if t.HourlyRate != nil {
		r := int64(*t.HourlyRate)
		rate = &r
	}
	row := q.QueryRow(ctx, `
		INSERT INTO project_tasks (org_id, project_id, name, hourly_rate, active)
		VALUES ($1, $2, $3, $4, true)
		RETURNING `+taskColumns,
		orgID, projectID, t.Name, rate,
	)
	created, err := scanTask(row)
	if isUniqueViolation(err) {
		return Task{}, fmt.Errorf("%w: a task named %q already exists in this project", core.ErrInvalidInput, t.Name)
	}
	return created, err
}

// ListTasks lists projectID's tasks, scoped to orgID, optionally restricted
// to active ones. It returns core.ErrNotFound if no such project exists in
// that org.
func ListTasks(ctx context.Context, q core.Querier, orgID, projectID int64, activeOnly bool) ([]Task, error) {
	if _, err := GetProject(ctx, q, orgID, projectID); err != nil {
		return nil, err
	}

	sql := `SELECT ` + taskColumns + ` FROM project_tasks WHERE org_id = $1 AND project_id = $2`
	if activeOnly {
		sql += " AND active"
	}
	sql += " ORDER BY id"

	rows, err := q.Query(ctx, sql, orgID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// GetTask looks up a task by id, scoped to orgID. It returns
// core.ErrNotFound if no such task exists in that org.
func GetTask(ctx context.Context, q core.Querier, orgID, id int64) (Task, error) {
	row := q.QueryRow(ctx, `SELECT `+taskColumns+` FROM project_tasks WHERE org_id = $1 AND id = $2`, orgID, id)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, core.ErrNotFound
	}
	return t, err
}

// UpdateTask validates t and updates name, hourly_rate (its override, which
// a nil HourlyRate clears) and active for the task id in orgID. It returns
// core.ErrNotFound if no such task exists in that org, or ErrInvalidInput
// for a duplicate name among the project's other tasks.
func UpdateTask(ctx context.Context, q core.Querier, orgID, id int64, t Task) (Task, error) {
	if err := validateTask(t); err != nil {
		return Task{}, err
	}
	existing, err := GetTask(ctx, q, orgID, id)
	if err != nil {
		return Task{}, err
	}

	var exists bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM project_tasks WHERE project_id = $1 AND name = $2 AND id != $3)`,
		existing.ProjectID, t.Name, id,
	).Scan(&exists); err != nil {
		return Task{}, err
	}
	if exists {
		return Task{}, fmt.Errorf("%w: a task named %q already exists in this project", core.ErrInvalidInput, t.Name)
	}

	var rate *int64
	if t.HourlyRate != nil {
		r := int64(*t.HourlyRate)
		rate = &r
	}
	row := q.QueryRow(ctx, `
		UPDATE project_tasks SET name = $1, hourly_rate = $2, active = $3
		WHERE org_id = $4 AND id = $5
		RETURNING `+taskColumns,
		t.Name, rate, t.Active, orgID, id,
	)
	updated, err := scanTask(row)
	if isUniqueViolation(err) {
		return Task{}, fmt.Errorf("%w: a task named %q already exists in this project", core.ErrInvalidInput, t.Name)
	}
	return updated, err
}
