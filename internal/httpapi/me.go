package httpapi

import (
	"log/slog"
	"net/http"

	"shigoto/internal/auth"
)

// meOrg is one org in GET /api/v1/me's orgs list.
type meOrg struct {
	Slug  string   `json:"slug"`
	Name  string   `json:"name"`
	Role  string   `json:"role"`
	Perms []string `json:"perms"`
	// Portal is true for a tenant or client membership (which otherwise
	// appears here with an empty Perms list, same as any other role with
	// no staff permissions): it tells the SPA this org belongs in the
	// portal nav rather than looking like a staff org with no access.
	Portal bool `json:"portal,omitempty"`
}

// registerMeRoutes mounts GET /api/v1/me: the current user's identity and
// every org they belong to, with the permissions their role in each grants.
// It never exposes the password hash or OIDC subject.
func registerMeRoutes(mux *http.ServeMux, d Deps) {
	mux.Handle("GET /api/v1/me", d.Auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var user struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		err := d.Pool.QueryRow(r.Context(),
			"SELECT id, email, name FROM users WHERE id = $1", userID,
		).Scan(&user.ID, &user.Email, &user.Name)
		if err != nil {
			slog.Error("resolving current user failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		rows, err := d.Pool.Query(r.Context(),
			`SELECT o.slug, o.name, m.role
			 FROM memberships m
			 JOIN orgs o ON o.id = m.org_id
			 WHERE m.user_id = $1
			 ORDER BY o.name`,
			userID,
		)
		if err != nil {
			slog.Error("listing current user's orgs failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer rows.Close()

		orgs := []meOrg{}
		for rows.Next() {
			var slug, name, role string
			if err := rows.Scan(&slug, &name, &role); err != nil {
				slog.Error("scanning current user's org row failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			perms := auth.PermsFor(auth.Role(role))
			permStrs := make([]string, len(perms))
			for i, p := range perms {
				permStrs[i] = string(p)
			}
			isPortal := role == string(auth.Tenant) || role == string(auth.Client)
			orgs = append(orgs, meOrg{Slug: slug, Name: name, Role: role, Perms: permStrs, Portal: isPortal})
		}
		if err := rows.Err(); err != nil {
			slog.Error("iterating current user's orgs failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"user": user,
			"orgs": orgs,
		})
	})))
}
