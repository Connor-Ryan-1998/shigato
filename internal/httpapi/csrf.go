package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// csrfMiddleware rejects unsafe (state-changing) requests to /api/ routes
// unless they carry the X-Shigoto-CSRF header. A cross-site form submission
// or a plain cross-origin <form> post can't set custom headers, so this
// header's presence proves the request came from shigoto's own SPA code,
// not a forged page riding on the browser's cookies.
//
// As a second layer, if the request carries an Origin header, it must match
// baseURL's scheme and host; a missing, unparsable or foreign Origin (which
// includes the literal "null" Origin browsers send for sandboxed iframes)
// is rejected. Safe methods and /webhooks/ (Stripe's server-to-server
// callback, which can't send either the header or an Origin) pass through
// untouched.
func csrfMiddleware(baseURL string, next http.Handler) http.Handler {
	base, _ := url.Parse(baseURL)
	baseOrigin := base.Scheme + "://" + base.Host

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		if r.Header.Get("X-Shigoto-CSRF") != "1" {
			writeError(w, http.StatusForbidden, "missing CSRF header")
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			o, err := url.Parse(origin)
			if err != nil || o.Scheme+"://"+o.Host != baseOrigin {
				writeError(w, http.StatusForbidden, "cross-site request blocked")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
