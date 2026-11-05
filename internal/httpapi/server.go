package httpapi

import (
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
)

// Module is a feature module that contributes routes to the API, gated by
// config.Config.Modules[Name].
type Module struct {
	Name   string
	Routes func(mux *http.ServeMux, d Deps)
}

// Deps holds the dependencies a module's Routes func needs to register its
// handlers.
type Deps struct {
	Pool *pgxpool.Pool
	Auth *auth.Auth
	Cfg  config.Config
}

// modules lists the feature modules available to mount. Plans 2, 3 and 4
// each append one entry. It is a package-level var (not a const slice) so
// tests can swap it out and restore it.
var modules = []Module{}

// New builds the HTTP handler: the core routes (auth, health, meta) plus
// each module in modules whose name is enabled in d.Cfg.Modules.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		d.Auth.LoginHandler(w, r)
	})
	mux.HandleFunc("POST /api/v1/auth/logout", d.Auth.LogoutHandler)

	var enabled []string
	for _, m := range modules {
		if d.Cfg.Modules[m.Name] {
			enabled = append(enabled, m.Name)
			m.Routes(mux, d)
		}
	}
	sort.Strings(enabled)
	if enabled == nil {
		enabled = []string{}
	}

	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"modules": enabled})
	})

	return mux
}
