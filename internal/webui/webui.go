// Package webui embeds and serves the built Svelte SPA (web/, built by
// `task web:build` into this package's dist directory) from the Go binary.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// notBuiltHTML is served in place of index.html when dist has no real
// build in it yet (a fresh checkout only commits dist/.gitkeep, so `go
// build`/`go test` work without Node - see .gitignore). It deliberately
// contains no inline <script>, so it needs no CSP script-src hash.
const notBuiltHTML = `<!doctype html>
<html lang="en">
	<head>
		<meta charset="utf-8" />
		<title>shigoto</title>
	</head>
	<body>
		Web UI not built — run <code>task web:build</code>
	</body>
</html>
`

// inlineScriptRe finds <script>...</script> blocks in the built index.html.
// It's a regexp, not an HTML parser, but index.html is build output we
// control (SvelteKit's own template), not arbitrary input, so this is safe
// for the one document it ever runs against.
var inlineScriptRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

// cspScriptHashes finds every *inline* <script> in index (one without a src
// attribute - an external <script src="...">, which the build also emits,
// is already covered by script-src 'self') and returns its CSP hash source,
// e.g. 'sha256-<base64>'.
func cspScriptHashes(index []byte) []string {
	var hashes []string
	for _, m := range inlineScriptRe.FindAllSubmatch(index, -1) {
		if strings.Contains(strings.ToLower(string(m[1])), "src=") {
			continue
		}
		sum := sha256.Sum256(m[2])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}

// buildCSP returns the SPA's Content-Security-Policy for the given
// index.html: scripts from the app's own origin plus a hash for each inline
// <script> the build emitted (SvelteKit's bootstrap loader has no src, so
// script-src 'self' alone would block it - ruling 42), styles also allow
// 'unsafe-inline' for Svelte's component-scoped <style> blocks, images also
// allow data: and blob: (file previews), nothing framed in or out, and no
// engine-rendered pages.
func buildCSP(index []byte) string {
	scriptSrc := "script-src 'self'"
	for _, h := range cspScriptHashes(index) {
		scriptSrc += " " + h
	}
	return "default-src 'self'; " + scriptSrc + "; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; " +
		"base-uri 'self'; form-action 'self'"
}

// Handler serves the embedded SPA: existing files under /_app/immutable/
// (content-hashed by the build, so safe to cache forever) get a one-year
// immutable Cache-Control; every other existing file, and index.html
// itself, get no-cache; any other path outside /_app/ falls back to
// index.html so client-side routing can handle deep links. An unmatched
// path under /_app/ is a genuinely missing asset, so it's a 404, not the
// SPA shell.
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
	index, err := fs.ReadFile(fsys, "index.html")
	built := err == nil
	if !built {
		index = []byte(notBuiltHTML)
	}
	csp := buildCSP(index)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// net/http decodes percent-escapes (e.g. %2e%2e) into r.URL.Path
		// before this handler ever sees it, so this also catches an
		// encoded "..".
		if strings.Contains(r.URL.Path, "..") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		clean := path.Clean(r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		underApp := strings.HasPrefix(name, "_app/")

		if built && name != "index.html" {
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

		if underApp {
			// A missing or directory path under /_app/ is a genuinely
			// absent asset (a stale reference to a previous build, a typo,
			// a probe) - never the SPA shell.
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Either "/" itself, index.html, or an unknown deep link (e.g.
		// /acme/invoices/42): serve the SPA shell (or, pre-build, the
		// placeholder) and let client-side routing take over.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	})
}
