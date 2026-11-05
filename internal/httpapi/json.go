// Package httpapi wires shigoto's HTTP API: the core auth/health/meta routes
// plus per-module routes gated by config.Config.Modules.
package httpapi

import (
	"net/http"

	"shigoto/internal/module"
)

// maxBodyBytes caps request bodies the login route reads directly (readJSON
// enforces its own copy of this limit via module.ReadJSON).
const maxBodyBytes = 1 << 20 // 1 MiB

// writeJSON, writeError and readJSON are thin wrappers over the module
// package's exported helpers, kept so httpapi's many call sites don't need
// a package-qualified rename.
func writeJSON(w http.ResponseWriter, status int, v any) { module.WriteJSON(w, status, v) }

func writeError(w http.ResponseWriter, status int, message string) {
	module.WriteError(w, status, message)
}

func readJSON(r *http.Request, v any) error { return module.ReadJSON(r, v) }
