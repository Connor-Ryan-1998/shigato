// Package webui embeds and serves the built Svelte SPA (web/, built by
// `task web:build` into this package's dist directory) from the Go binary.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// csp is the SPA's Content-Security-Policy: scripts and styles only from
// the app's own origin (styles allow 'unsafe-inline' for Svelte's
// component-scoped <style> blocks, which inject inline <style> tags),
// images also from data: and blob: (file previews), nothing framed in or
// out, and no engine-rendered pages.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; " +
	"base-uri 'self'; form-action 'self'"

// Handler serves the embedded SPA: existing files under /_app/immutable/
// (content-hashed by the build, so safe to cache forever) get a one-year
// immutable Cache-Control; every other existing file, and index.html
// itself, get no-cache; any other path falls back to index.html so
// client-side routing can handle deep links.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// The embed directive guarantees dist exists at build time; a
		// failure here means the embed itself is broken, not a runtime
		// condition callers can recover from.
		panic(err)
	}
	return handlerFS(sub)
}

// handlerFS is Handler's logic over an injected fs.FS, so tests can swap in
// an fstest.MapFS instead of the real embedded build output.
func handlerFS(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")

		if strings.Contains(r.URL.Path, "..") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "index.html" {
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				http.ServeFileFS(w, r, fsys, name)
				return
			}
		}

		// Either "/" itself, index.html, or an unknown deep link (e.g.
		// /acme/invoices/42): serve the SPA shell and let client-side
		// routing take over.
		w.Header().Set("Cache-Control", "no-cache")
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}
