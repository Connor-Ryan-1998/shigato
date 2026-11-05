package timesheets

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

// dateLayout matches httpapi's own date encoding (YYYY-MM-DD).
const dateLayout = "2006-01-02"

// parseDate parses an RFC 3339 date (YYYY-MM-DD) as UTC midnight.
func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation(dateLayout, s, time.UTC)
}

// clockNow is time.Now, overridable by tests so they can pin "today" to a
// specific instant. See internal/property/routes.go's clockNow/today for
// the identical pattern this mirrors; it's duplicated here (rather than
// shared) because it's ten lines and moving it would touch property's
// tests for no benefit to either module.
var clockNow = time.Now

// today returns today's calendar date in d.Cfg.Location (defaulting to
// UTC), as a UTC midnight - the same units parseDate produces.
func today(d module.Deps) time.Time {
	loc := d.Cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, dd := clockNow().In(loc).Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
}

// Module returns the timesheets module: projects, tasks and (in a later
// task) time entries, gated by config.Config.Modules["timesheets"].
func Module() module.Module {
	return module.Module{Name: "timesheets", Routes: registerRoutes}
}

func registerRoutes(mux *http.ServeMux, d module.Deps) {
	mux.Handle("POST /api/v1/orgs/{org}/projects",
		d.Auth.RequireOrg(auth.PermTimesheetsAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createProject(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/projects",
		d.Auth.RequireOrg(auth.PermTimesheetsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listProjects(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/projects/{id}",
		d.Auth.RequireOrg(auth.PermTimesheetsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getProject(w, r, d)
		})))
	mux.Handle("PUT /api/v1/orgs/{org}/projects/{id}",
		d.Auth.RequireOrg(auth.PermTimesheetsAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			updateProject(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/projects/{id}/tasks",
		d.Auth.RequireOrg(auth.PermTimesheetsAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createTask(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/projects/{id}/tasks",
		d.Auth.RequireOrg(auth.PermTimesheetsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listTasks(w, r, d)
		})))
	mux.Handle("PUT /api/v1/orgs/{org}/tasks/{id}",
		d.Auth.RequireOrg(auth.PermTimesheetsAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			updateTask(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/time-entries",
		d.Auth.RequireOrg(auth.PermTimesheetsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createEntry(w, r, d)
		})))
	mux.Handle("PUT /api/v1/orgs/{org}/time-entries/{id}",
		d.Auth.RequireOrg(auth.PermTimesheetsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			updateEntry(w, r, d)
		})))
	mux.Handle("DELETE /api/v1/orgs/{org}/time-entries/{id}",
		d.Auth.RequireOrg(auth.PermTimesheetsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deleteEntry(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/time-entries",
		d.Auth.RequireOrg(auth.PermTimesheetsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listEntries(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/billing-runs",
		d.Auth.RequireOrg(auth.PermInvoicesWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			runBilling(w, r, d)
		})))
}

// --- request/response shapes ---

type projectRequest struct {
	Name            string `json:"name"`
	ClientContactID int64  `json:"client_contact_id"`
	HourlyRate      int64  `json:"hourly_rate"`
	GSTCode         string `json:"gst_code,omitempty"`
}

// projectUpdateRequest is a full replace: every field must be present, so
// pointers detect an omitted key (handled as a 422, never silently
// defaulted - unlike projectRequest's create-only gst_code default).
type projectUpdateRequest struct {
	Name       *string `json:"name"`
	HourlyRate *int64  `json:"hourly_rate"`
	GSTCode    *string `json:"gst_code"`
	Active     *bool   `json:"active"`
}

type projectResponse struct {
	ID              int64  `json:"id"`
	ClientContactID int64  `json:"client_contact_id"`
	Name            string `json:"name"`
	HourlyRate      int64  `json:"hourly_rate"`
	GSTCode         string `json:"gst_code"`
	Active          bool   `json:"active"`
}

func projectToResponse(p Project) projectResponse {
	return projectResponse{
		ID: p.ID, ClientContactID: p.ClientContactID, Name: p.Name,
		HourlyRate: int64(p.HourlyRate), GSTCode: string(p.GSTCode), Active: p.Active,
	}
}

type taskRequest struct {
	Name       string `json:"name"`
	HourlyRate *int64 `json:"hourly_rate,omitempty"`
}

// taskUpdateRequest is a full replace: Name and Active must be present
// (pointers detect an omitted key), and HourlyRate must be present too -
// json.RawMessage distinguishes an omitted key (nil) from an explicit
// JSON null (the literal bytes "null", which clears the rate override)
// from a number, which plain *int64 can't do since both "absent" and
// "null" decode to the same nil pointer.
type taskUpdateRequest struct {
	Name       *string         `json:"name"`
	HourlyRate json.RawMessage `json:"hourly_rate"`
	Active     *bool           `json:"active"`
}

type taskResponse struct {
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"project_id"`
	Name       string `json:"name"`
	HourlyRate *int64 `json:"hourly_rate,omitempty"`
	Active     bool   `json:"active"`
}

func taskToResponse(t Task) taskResponse {
	resp := taskResponse{ID: t.ID, ProjectID: t.ProjectID, Name: t.Name, Active: t.Active}
	if t.HourlyRate != nil {
		r := int64(*t.HourlyRate)
		resp.HourlyRate = &r
	}
	return resp
}

// --- projects handlers ---

func createProject(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body projectRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	gstCode := core.GSTCode(body.GSTCode)
	if body.GSTCode == "" {
		gstCode = core.GST
	}

	p := auth.From(r.Context())
	var created Project
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateProject(r.Context(), tx, p.OrgID, Project{
			ClientContactID: body.ClientContactID, Name: body.Name,
			HourlyRate: core.Cents(body.HourlyRate), GSTCode: gstCode,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "project", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, projectToResponse(created))
}

func listProjects(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var clientID *int64
	if s := r.URL.Query().Get("client_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "client_id must be an integer")
			return
		}
		clientID = &id
	}
	activeOnly := r.URL.Query().Get("active") == "true"

	p := auth.From(r.Context())
	projects, err := ListProjects(r.Context(), d.Pool, p.OrgID, clientID, activeOnly)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]projectResponse, len(projects))
	for i, pr := range projects {
		resp[i] = projectToResponse(pr)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func getProject(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	proj, err := GetProject(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, projectToResponse(proj))
}

func updateProject(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body projectUpdateRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch {
	case body.Name == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "name is required")
		return
	case body.HourlyRate == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "hourly_rate is required")
		return
	case body.GSTCode == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "gst_code is required")
		return
	case body.Active == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "active is required")
		return
	}

	p := auth.From(r.Context())
	var updated Project
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = UpdateProject(r.Context(), tx, p.OrgID, id, Project{
			Name: *body.Name, HourlyRate: core.Cents(*body.HourlyRate),
			GSTCode: core.GSTCode(*body.GSTCode), Active: *body.Active,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "project", updated.ID, "update", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, projectToResponse(updated))
}

// --- tasks handlers ---

func createTask(w http.ResponseWriter, r *http.Request, d module.Deps) {
	projectID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body taskRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rate *core.Cents
	if body.HourlyRate != nil {
		c := core.Cents(*body.HourlyRate)
		rate = &c
	}

	p := auth.From(r.Context())
	var created Task
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateTask(r.Context(), tx, p.OrgID, projectID, Task{
			ProjectID: projectID, Name: body.Name, HourlyRate: rate,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "project_task", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, taskToResponse(created))
}

func listTasks(w http.ResponseWriter, r *http.Request, d module.Deps) {
	projectID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	activeOnly := r.URL.Query().Get("active") == "true"

	p := auth.From(r.Context())
	tasks, err := ListTasks(r.Context(), d.Pool, p.OrgID, projectID, activeOnly)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]taskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func updateTask(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body taskUpdateRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}
	if body.Active == nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "active is required")
		return
	}
	if body.HourlyRate == nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "hourly_rate is required (use null to clear the override)")
		return
	}
	var rate *core.Cents
	if string(body.HourlyRate) != "null" {
		var r64 int64
		if err := json.Unmarshal(body.HourlyRate, &r64); err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "hourly_rate must be a number or null")
			return
		}
		c := core.Cents(r64)
		rate = &c
	}

	p := auth.From(r.Context())
	var updated Task
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = UpdateTask(r.Context(), tx, p.OrgID, id, Task{
			Name: *body.Name, HourlyRate: rate, Active: *body.Active,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "project_task", updated.ID, "update", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, taskToResponse(updated))
}

// --- time entries handlers ---

type entryRequest struct {
	TaskID   int64  `json:"task_id"`
	WorkDate string `json:"work_date"`
	Minutes  int    `json:"minutes"`
	Note     string `json:"note,omitempty"`
}

// entryUpdateRequest is a full replace: every field must be present, so
// pointers detect an omitted key (handled as a 422), following the same
// pattern as projectUpdateRequest/taskUpdateRequest. user_id is never
// accepted: it can't be changed.
type entryUpdateRequest struct {
	TaskID   *int64  `json:"task_id"`
	WorkDate *string `json:"work_date"`
	Minutes  *int    `json:"minutes"`
	Note     *string `json:"note"`
}

type entryResponse struct {
	ID            int64  `json:"id"`
	TaskID        int64  `json:"task_id"`
	UserID        int64  `json:"user_id"`
	WorkDate      string `json:"work_date"`
	Minutes       int    `json:"minutes"`
	Note          string `json:"note"`
	InvoiceLineID *int64 `json:"invoice_line_id,omitempty"`
}

func entryToResponse(e Entry) entryResponse {
	return entryResponse{
		ID: e.ID, TaskID: e.TaskID, UserID: e.UserID,
		WorkDate: e.WorkDate.Format(dateLayout), Minutes: e.Minutes, Note: e.Note,
		InvoiceLineID: e.InvoiceLineID,
	}
}

func createEntry(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body entryRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	workDate, err := parseDate(body.WorkDate)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "work_date must be YYYY-MM-DD")
		return
	}

	p := auth.From(r.Context())
	var created Entry
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateEntry(r.Context(), tx, p.OrgID, p.UserID, Entry{
			TaskID: body.TaskID, WorkDate: workDate, Minutes: body.Minutes, Note: body.Note,
		}, today(d))
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "time_entry", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, entryToResponse(created))
}

func updateEntry(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body entryUpdateRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch {
	case body.TaskID == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "task_id is required")
		return
	case body.WorkDate == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "work_date is required")
		return
	case body.Minutes == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "minutes is required")
		return
	case body.Note == nil:
		module.WriteError(w, http.StatusUnprocessableEntity, "note is required")
		return
	}
	workDate, err := parseDate(*body.WorkDate)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "work_date must be YYYY-MM-DD")
		return
	}

	p := auth.From(r.Context())
	var updated Entry
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = UpdateEntry(r.Context(), tx, p.OrgID, p, id, Entry{
			TaskID: *body.TaskID, WorkDate: workDate, Minutes: *body.Minutes, Note: *body.Note,
		}, today(d))
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "time_entry", updated.ID, "update", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, entryToResponse(updated))
}

func deleteEntry(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		if err := DeleteEntry(r.Context(), tx, p.OrgID, p, id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "time_entry", id, "delete", nil)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type entryListResponse struct {
	Entries      []entryResponse `json:"entries"`
	TotalMinutes int             `json:"total_minutes"`
}

func listEntries(w http.ResponseWriter, r *http.Request, d module.Deps) {
	q := r.URL.Query()
	var filter EntryFilter

	parseID := func(name string) (*int64, bool) {
		s := q.Get(name)
		if s == "" {
			return nil, true
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, false
		}
		return &id, true
	}
	var ok bool
	if filter.TaskID, ok = parseID("task_id"); !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, "task_id must be an integer")
		return
	}
	if filter.ProjectID, ok = parseID("project_id"); !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, "project_id must be an integer")
		return
	}
	if filter.UserID, ok = parseID("user_id"); !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, "user_id must be an integer")
		return
	}
	if filter.ClientID, ok = parseID("client_id"); !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, "client_id must be an integer")
		return
	}
	if s := q.Get("from"); s != "" {
		t, err := parseDate(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "from must be YYYY-MM-DD")
			return
		}
		filter.From = &t
	}
	if s := q.Get("to"); s != "" {
		t, err := parseDate(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "to must be YYYY-MM-DD")
			return
		}
		filter.To = &t
	}
	if s := q.Get("unbilled"); s != "" {
		if s != "true" {
			module.WriteError(w, http.StatusUnprocessableEntity, "unbilled must be true")
			return
		}
		filter.Unbilled = true
	}

	p := auth.From(r.Context())
	entries, err := ListEntries(r.Context(), d.Pool, p.OrgID, filter)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := entryListResponse{Entries: make([]entryResponse, len(entries))}
	for i, e := range entries {
		resp.Entries[i] = entryToResponse(e)
		resp.TotalMinutes += e.Minutes
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

// --- billing runs handler ---

type billingRunRequest struct {
	ClientContactID int64  `json:"client_contact_id"`
	From            string `json:"from"`
	To              string `json:"to"`
	DueInDays       int    `json:"due_in_days"`
	Issue           bool   `json:"issue"`
}

type billingRunResponse struct {
	Invoice      core.Invoice `json:"invoice"`
	EntryCount   int          `json:"entry_count"`
	TotalMinutes int          `json:"total_minutes"`
}

func runBilling(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body billingRunRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	from, err := parseDate(body.From)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "from must be YYYY-MM-DD")
		return
	}
	to, err := parseDate(body.To)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "to must be YYYY-MM-DD")
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	result, err := RunBilling(ctx, d.Billing, p.OrgID, r.PathValue("org"), RunInput{
		ClientContactID: body.ClientContactID, From: from, To: to, DueInDays: body.DueInDays, Issue: body.Issue,
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, billingRunResponse{
		Invoice: result.Invoice, EntryCount: result.EntryCount, TotalMinutes: result.TotalMinutes,
	})
}
