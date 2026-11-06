package httpapi

import "net/http"

// jsonAPIFallback wraps mux (which only ever registers /healthz and
// specific /api/ and /webhooks/ paths, never a "/" pattern - see
// routeWebUI) so that an unmatched /api/ path, or a method a known /api/
// path doesn't support, comes back as this API's {"error": "..."} JSON
// instead of net/http.ServeMux's plain-text "404 page not found" / "Method
// Not Allowed".
//
// mux.Handler(r) reports whether r matches a registered pattern without
// running anything: when it does, the request is routed straight to mux
// and streamed through untouched - this matters for responses this
// wrapper must never buffer into memory, like a CSV export or a file
// download. Only the unmatched case (pattern == "") is replaced, and even
// then by reusing mux's own NotFound/MethodNotAllowed handler for the
// status and (on 405) the Allow header, since that handler's body is
// always one short, fixed string - cheap to capture and discard.
func jsonAPIFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}

		h, _ := mux.Handler(r)
		rec := &statusRecorder{header: make(http.Header)}
		h.ServeHTTP(rec, r)

		dst := w.Header()
		for k, v := range rec.header {
			dst[k] = v
		}
		if rec.status == http.StatusMethodNotAllowed {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	})
}

// statusRecorder is a minimal http.ResponseWriter that discards the body
// and captures only the header and status, for the small, fixed-body
// NotFound/MethodNotAllowed responses jsonAPIFallback replaces.
type statusRecorder struct {
	header http.Header
	status int
}

func (s *statusRecorder) Header() http.Header { return s.header }

func (s *statusRecorder) Write(p []byte) (int, error) { return len(p), nil }

func (s *statusRecorder) WriteHeader(status int) { s.status = status }
