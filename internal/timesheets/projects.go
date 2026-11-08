package timesheets

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

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

const projectColumns = `id, org_id, client_contact_id, name, hourly_rate, gst_code,
	status, description, start_date, end_date, billing_method, active`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	var gstCode string
	err := row.Scan(&p.ID, &p.OrgID, &p.ClientContactID, &p.Name, &p.HourlyRate, &gstCode,
		&p.Status, &p.Description, &p.StartDate, &p.EndDate, &p.BillingMethod, &p.Active)
	p.GSTCode = core.GSTCode(gstCode)
	return p, err
}

// validateProject checks the fields CreateProject/UpdateProject share. The
// client contact is validated separately by checkClient.
func validateProject(p Project) error {
	if p.Name == "" {
		return fmt.Errorf("%w: name is required", core.ErrInvalidInput)
	}
	if p.HourlyRate <= 0 {
		return fmt.Errorf("%w: hourly_rate must be positive", core.ErrInvalidInput)
	}
	switch p.Status {
	case StatusPending, StatusInProgress, StatusCompleted, StatusCancelled:
	default:
		return fmt.Errorf("%w: status must be pending, in_progress, completed or cancelled", core.ErrInvalidInput)
	}
	switch p.BillingMethod {
	case BillingTM, BillingFixed:
	default:
		return fmt.Errorf("%w: billing_method must be tm or fixed", core.ErrInvalidInput)
	}
	if utf8.RuneCountInString(p.Description) > 2000 {
		return fmt.Errorf("%w: description must be at most 2000 characters", core.ErrInvalidInput)
	}
	if p.StartDate != nil && p.EndDate != nil && p.EndDate.Before(*p.StartDate) {
		return fmt.Errorf("%w: end_date must not be before start_date", core.ErrInvalidInput)
	}
	return validateGSTCode(p.GSTCode)
}

// checkClient requires contactID to be a contact of kind "client" in orgID,
// otherwise ErrInvalidInput.
func checkClient(ctx context.Context, q core.Querier, orgID, contactID int64) error {
	contact, err := core.GetContact(ctx, q, orgID, contactID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("%w: client_contact_id not found", core.ErrInvalidInput)
		}
		return err
	}
	if contact.Kind != "client" {
		return fmt.Errorf("%w: client_contact_id is not a client", core.ErrInvalidInput)
	}
	return nil
}

// CreateProject validates p and inserts it for orgID. p.ClientContactID
// must be a contact of kind "client" in orgID, otherwise ErrInvalidInput. A
// duplicate name in the org also gives ErrInvalidInput, checked before
// insert and backstopped against the unique_violation a concurrent request
// could race into. An empty Status defaults to in progress and an empty
// BillingMethod to time and materials.
func CreateProject(ctx context.Context, q core.Querier, orgID int64, p Project) (Project, error) {
	if p.Status == "" {
		p.Status = StatusInProgress
	}
	if p.BillingMethod == "" {
		p.BillingMethod = BillingTM
	}
	if err := validateProject(p); err != nil {
		return Project{}, err
	}
	if err := checkClient(ctx, q, orgID, p.ClientContactID); err != nil {
		return Project{}, err
	}

	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE org_id = $1 AND name = $2)`, orgID, p.Name).Scan(&exists); err != nil {
		return Project{}, err
	}
	if exists {
		return Project{}, fmt.Errorf("%w: a project named %q already exists", core.ErrInvalidInput, p.Name)
	}

	row := q.QueryRow(ctx, `
		INSERT INTO projects (org_id, client_contact_id, name, hourly_rate, gst_code,
			status, description, start_date, end_date, billing_method)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+projectColumns,
		orgID, p.ClientContactID, p.Name, int64(p.HourlyRate), string(p.GSTCode),
		p.Status, p.Description, p.StartDate, p.EndDate, p.BillingMethod,
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

// UpdateProject validates p and replaces every editable field of the
// project id in orgID (p.Active is ignored: it follows Status). The client
// contact may only change while none of the project's time has been billed,
// so an issued invoice never ends up under a project for another customer
// (core.ErrInvalidState otherwise). It returns core.ErrNotFound if no such
// project exists in that org, or ErrInvalidInput for a duplicate name among
// the org's other projects.
func UpdateProject(ctx context.Context, q core.Querier, orgID, id int64, p Project) (Project, error) {
	if err := validateProject(p); err != nil {
		return Project{}, err
	}
	existing, err := GetProject(ctx, q, orgID, id)
	if err != nil {
		return Project{}, err
	}
	if p.ClientContactID != existing.ClientContactID {
		if err := checkClient(ctx, q, orgID, p.ClientContactID); err != nil {
			return Project{}, err
		}
		var billed bool
		if err := q.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM time_entries e JOIN project_tasks t ON t.id = e.task_id
				WHERE e.org_id = $1 AND t.project_id = $2 AND e.invoice_line_id IS NOT NULL
			)`, orgID, id,
		).Scan(&billed); err != nil {
			return Project{}, err
		}
		if billed {
			return Project{}, fmt.Errorf("%w: the customer can't change once project time has been invoiced", core.ErrInvalidState)
		}
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
		UPDATE projects SET name = $1, hourly_rate = $2, gst_code = $3, status = $4,
			description = $5, start_date = $6, end_date = $7, billing_method = $8,
			client_contact_id = $9
		WHERE org_id = $10 AND id = $11
		RETURNING `+projectColumns,
		p.Name, int64(p.HourlyRate), string(p.GSTCode), p.Status,
		p.Description, p.StartDate, p.EndDate, p.BillingMethod,
		p.ClientContactID, orgID, id,
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
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM project_tasks WHERE org_id = $1 AND project_id = $2 AND name = $3)`,
		orgID, projectID, t.Name,
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
		`SELECT EXISTS(SELECT 1 FROM project_tasks WHERE org_id = $1 AND project_id = $2 AND name = $3 AND id != $4)`,
		orgID, existing.ProjectID, t.Name, id,
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
