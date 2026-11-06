package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
)

// clockNow is time.Now, overridable by tests so they can pin "today" to a
// specific instant. See internal/property/routes.go's clockNow/today for
// the identical pattern this mirrors.
var clockNow = time.Now

// today returns today's calendar date in d.Cfg.Location (defaulting to
// UTC), as a UTC midnight - the same convention property.today and
// timesheets.today use for "today" throughout the app.
func today(d Deps) time.Time {
	loc := d.Cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, dd := clockNow().In(loc).Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
}

// dashboardModulePerm maps a dashboard module section to the read
// permission a role must hold to see it. Reconciliation's own GET routes
// (bank lines, categories, ...) are themselves gated by PermReportsRead,
// not PermReconWrite, so the dashboard follows the same read/write split.
var dashboardModulePerm = map[string]auth.Perm{
	"property":       auth.PermPropertyRead,
	"timesheets":     auth.PermTimesheetsRead,
	"reconciliation": auth.PermReportsRead,
}

// coreDashboard is the "core" section of GET /dashboard: invoice counts and
// outstanding balances, always present regardless of which modules are
// enabled.
type coreDashboard struct {
	OpenCount          int   `json:"open_count"`
	OpenOutstanding    int64 `json:"open_outstanding"`
	OverdueCount       int   `json:"overdue_count"`
	OverdueOutstanding int64 `json:"overdue_outstanding"`
	DraftCount         int   `json:"draft_count"`
}

// buildCoreDashboard computes coreDashboard in a single aggregate query: a
// per-invoice outstanding (total minus its payments, floored at 0, via
// GREATEST) is computed in a subquery, then summed/counted per status in
// the outer query's FILTER clauses - one round trip regardless of how many
// invoices the org has.
func buildCoreDashboard(ctx context.Context, q core.Querier, orgID int64, today time.Time) (coreDashboard, error) {
	var d coreDashboard
	err := q.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'draft') AS draft_count,
			count(*) FILTER (WHERE status = 'open') AS open_count,
			coalesce(sum(GREATEST(total - paid, 0)) FILTER (WHERE status = 'open'), 0) AS open_outstanding,
			count(*) FILTER (WHERE status = 'open' AND due_date < $2) AS overdue_count,
			coalesce(sum(GREATEST(total - paid, 0)) FILTER (WHERE status = 'open' AND due_date < $2), 0) AS overdue_outstanding
		FROM (
			SELECT i.status, i.due_date, i.total,
				coalesce((SELECT sum(amount) FROM payments p WHERE p.org_id = i.org_id AND p.invoice_id = i.id), 0) AS paid
			FROM invoices i WHERE i.org_id = $1
		) s`,
		orgID, today,
	).Scan(&d.DraftCount, &d.OpenCount, &d.OpenOutstanding, &d.OverdueCount, &d.OverdueOutstanding)
	return d, err
}

// registerDashboardRoutes mounts GET /dashboard: a core-always, read-gated
// endpoint rather than a module route, since it assembles sections across
// every module.
func registerDashboardRoutes(mux *http.ServeMux, d Deps) {
	mux.Handle("GET /api/v1/orgs/{org}/dashboard",
		d.Auth.RequireOrg(auth.PermInvoicesRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dashboardHandler(w, r, d)
		})))
}

// dashboardHandler assembles GET /dashboard's response: the core section,
// plus one section per enabled module whose Dashboard is non-nil and whose
// read permission the caller's role holds. A module failing either check
// is silently omitted, per the task's contract - not a 403, since the rest
// of the dashboard is still valid for the caller.
func dashboardHandler(w http.ResponseWriter, r *http.Request, d Deps) {
	p := auth.From(r.Context())
	now := today(d)

	coreSection, err := buildCoreDashboard(r.Context(), d.Pool, p.OrgID, now)
	if err != nil {
		slog.Error("dashboard core section failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	sections := map[string]any{}
	for _, m := range modules {
		if !d.Cfg.Modules[m.Name] || m.Dashboard == nil {
			continue
		}
		perm, ok := dashboardModulePerm[m.Name]
		if !ok || !auth.Can(p.Role, perm) {
			continue
		}
		section, err := m.Dashboard(r.Context(), d.Pool, p.OrgID, now)
		if err != nil {
			slog.Error("dashboard module section failed", "module", m.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		sections[m.Name] = section
	}

	writeJSON(w, http.StatusOK, map[string]any{"core": coreSection, "modules": sections})
}
