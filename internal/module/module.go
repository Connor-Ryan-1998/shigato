// Package module defines the shared contract feature modules (property,
// timesheets, reconciliation, ...) use to register routes into httpapi's
// router, plus the JSON/HTTP helpers those routes and httpapi's own core
// routes share. It exists separately from httpapi so modules (e.g.
// property) can depend on it without httpapi depending on them, avoiding an
// import cycle.
package module

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/config"
	"shigoto/internal/core"
)

// Deps holds the dependencies a module's Routes func needs to register its
// handlers.
type Deps struct {
	Pool    *pgxpool.Pool
	Auth    *auth.Auth
	Cfg     config.Config
	Billing *billing.Service
}

// Module is a feature module that contributes routes to the API, gated by
// config.Config.Modules[Name].
type Module struct {
	Name   string
	Routes func(mux *http.ServeMux, d Deps)
}

// maxBodyBytes caps request bodies ReadJSON will accept.
const maxBodyBytes = 1 << 20 // 1 MiB

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes a JSON error body {"error":"<message>"} with the given
// status code.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// ReadJSON decodes a JSON request body into v. The body is limited to
// maxBodyBytes and unknown fields are rejected.
func ReadJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// RunInTx runs fn in a new transaction on pool, committing on success and
// rolling back (a no-op once committed) otherwise.
func RunInTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PathID parses the {id} path value as an int64. A malformed id is treated
// like an unknown one: the caller should respond 404, not reveal that the
// path shape mattered.
func PathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// WriteCoreError maps a core package error to an HTTP response.
func WriteCoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not found")
	case errors.Is(err, core.ErrForbidden):
		WriteError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, core.ErrInvalidInput):
		WriteError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, core.ErrInvalidState):
		WriteError(w, http.StatusConflict, "invalid state")
	default:
		slog.Error("request failed", "error", err)
		WriteError(w, http.StatusInternalServerError, "internal error")
	}
}
