package timesheets

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

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
}

// --- request/response shapes ---

type projectRequest struct {
	Name            string `json:"name"`
	ClientContactID int64  `json:"client_contact_id"`
	HourlyRate      int64  `json:"hourly_rate"`
	GSTCode         string `json:"gst_code,omitempty"`
	Active          *bool  `json:"active,omitempty"`
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
	HourlyRate *int64 `json:"hourly_rate"`
	Active     *bool  `json:"active,omitempty"`
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
	var body projectRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}

	p := auth.From(r.Context())
	var updated Project
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = UpdateProject(r.Context(), tx, p.OrgID, id, Project{
			Name: body.Name, HourlyRate: core.Cents(body.HourlyRate),
			GSTCode: core.GSTCode(body.GSTCode), Active: active,
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
	var body taskRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}
	var rate *core.Cents
	if body.HourlyRate != nil {
		c := core.Cents(*body.HourlyRate)
		rate = &c
	}

	p := auth.From(r.Context())
	var updated Task
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = UpdateTask(r.Context(), tx, p.OrgID, id, Task{
			Name: body.Name, HourlyRate: rate, Active: active,
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
