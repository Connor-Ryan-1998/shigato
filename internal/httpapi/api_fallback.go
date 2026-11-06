package httpapi

import (
	"bytes"
	"net/http"
)

// jsonAPIFallback wraps an /api/-only handler (no "/" pattern registered on
// it - see routeWebUI) so that an unmatched path or a method the path
// doesn't support comes back as this API's {"error": "..."} JSON, not
// net/http.ServeMux's plain-text "404 page not found" / "Method Not
// Allowed".
//
// It buffers the whole response to do this: there's no way to know a
// handler settled on 404/405 until it has already written its body, and by
// then it's too late to still set the real Content-Type or status line on
// w. Responses this small make buffering the simplest correct option.
func jsonAPIFallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &bufferedResponse{header: make(http.Header)}
		next.ServeHTTP(rec, r)

		switch rec.status {
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "not found")
			return
		case http.StatusMethodNotAllowed:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		dst := w.Header()
		for k, v := range rec.header {
			dst[k] = v
		}
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	})
}

// bufferedResponse is a minimal http.ResponseWriter that captures a
// response instead of sending it, so jsonAPIFallback can inspect the status
// before deciding whether to replace the body.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

func (b *bufferedResponse) WriteHeader(status int) { b.status = status }
