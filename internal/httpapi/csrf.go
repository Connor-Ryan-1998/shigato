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
	// config.Load validates SHIGOTO_BASE_URL is an absolute http(s) URL
	// before this ever runs in production, but don't trust that blindly
	// here: url.Parse returns a nil *url.URL on error, and dereferencing it
	// would panic on every request rather than just denying cross-origin
	// ones. An empty baseOrigin can never equal a parsed request Origin, so
	// a bad baseURL fails safe (every Origin-bearing request is rejected)
	// rather than panicking.
	var baseOrigin string
	if base, err := url.Parse(baseURL); err == nil {
		baseOrigin = normalizeOrigin(base.Scheme, base.Host)
	}

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
			if err != nil || baseOrigin == "" || normalizeOrigin(o.Scheme, o.Host) != baseOrigin {
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

// normalizeOrigin lowercases scheme and host (hostnames are case-insensitive,
// and a browser-sent Origin or an operator-typed BaseURL may disagree on
// case) and strips the scheme's default port, so "https://App.Example.com"
// and "https://app.example.com:443" compare equal to
// "https://app.example.com".
func normalizeOrigin(scheme, host string) string {
	scheme = strings.ToLower(scheme)
	host = strings.ToLower(host)
	if scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	if scheme == "http" {
		host = strings.TrimSuffix(host, ":80")
	}
	return scheme + "://" + host
}
