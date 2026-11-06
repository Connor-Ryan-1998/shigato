package portal

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

// RegisterRoutes mounts the contact-scoped portal API: GET
// /api/v1/portal/orgs (any signed-in portal user, across every org they
// belong to) and GET /api/v1/portal/{org}/invoices[/{id}] and
// /api/v1/portal/{org}/leases, each scoped to the signed-in membership's own
// contact via auth.Auth.RequirePortal. Unlike the module registry, this is
// always on: it isn't gated by config.Config.Modules.
func RegisterRoutes(mux *http.ServeMux, d module.Deps) {
	mux.Handle("GET /api/v1/portal/orgs", d.Auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listOrgs(w, r, d)
	})))

	mux.Handle("GET /api/v1/portal/{org}/invoices", d.Auth.RequirePortal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listInvoices(w, r, d)
	})))
	mux.Handle("GET /api/v1/portal/{org}/invoices/{id}", d.Auth.RequirePortal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getInvoice(w, r, d)
	})))
	mux.Handle("GET /api/v1/portal/{org}/leases", d.Auth.RequirePortal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listLeases(w, r, d)
	})))
}

func listOrgs(w http.ResponseWriter, r *http.Request, d module.Deps) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		module.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	orgs, err := ListOrgs(r.Context(), d.Pool, userID)
	if err != nil {
		slog.Error("listing portal orgs failed", "error", err)
		module.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	module.WriteJSON(w, http.StatusOK, orgs)
}

func listInvoices(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	if p.ContactID == nil {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	invoices, err := ListInvoices(r.Context(), d.Pool, d.Billing, p.OrgID, *p.ContactID)
	if err != nil {
		slog.Error("listing portal invoices failed", "error", err)
		module.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	module.WriteJSON(w, http.StatusOK, invoices)
}

func getInvoice(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	if p.ContactID == nil {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	detail, err := GetInvoiceDetail(r.Context(), d.Pool, d.Billing, p.OrgID, *p.ContactID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, detail)
}

func listLeases(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	// Leases are tenant-only (global-constraints.md: "Tenants see only
	// leases..."); a client membership - valid for the invoices routes -
	// gets the same 404 as no membership at all, per the "portal 404s"
	// ruling.
	if p.Role != auth.Tenant || p.ContactID == nil {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}

	loc := d.Cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, dd := time.Now().In(loc).Date()
	today := time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)

	leases, err := ListLeases(r.Context(), d.Pool, p.OrgID, *p.ContactID, today)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			module.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		slog.Error("listing portal leases failed", "error", err)
		module.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	module.WriteJSON(w, http.StatusOK, leases)
}
