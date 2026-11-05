// Package httpapi wires shigoto's HTTP API: the core auth/health/meta routes
// plus per-module routes gated by config.Config.Modules.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// maxBodyBytes caps request bodies readJSON (and the login route) will
// accept.
const maxBodyBytes = 1 << 20 // 1 MiB

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error body {"error":"<message>"} with the given
// status code.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// readJSON decodes a JSON request body into v. The body is limited to
// maxBodyBytes and unknown fields are rejected.
func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
