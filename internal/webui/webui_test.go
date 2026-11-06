package webui

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexWithInlineScript = `<!doctype html>
<html>
<head>
<script>console.log("inline bootstrap")</script>
<script src="/_app/immutable/entry/start.js"></script>
</head>
<body></body>
</html>
`

func TestWebUIServesIndexForDeepLink(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/acme/invoices/42", nil)
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("CSP header is empty")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}

func TestWebUIImmutableAssets(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":                 {Data: []byte("<html></html>")},
		"_app/immutable/chunks/a.js": {Data: []byte("console.log(1)")},
		"favicon.ico":                {Data: []byte("icon")},
	}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_app/immutable/chunks/a.js", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("immutable asset: status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("immutable asset Cache-Control = %q", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("plain asset: status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("plain asset Cache-Control = %q, want no-cache", got)
	}
}

func TestWebUIRejectsDotDot(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/../../etc/passwd", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestWebUIRejectsEncodedDotDot checks a percent-encoded ".." is caught too:
// net/http decodes it into r.URL.Path before the handler ever runs.
func TestWebUIRejectsEncodedDotDot(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/%2e%2e/%2e%2e/etc/passwd", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestWebUICSPIncludesInlineScriptHash is ruling 42: script-src 'self'
// alone blocks SvelteKit's inline bootstrap <script>, so the CSP must carry
// a hash matching it exactly.
func TestWebUICSPIncludesInlineScriptHash(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(indexWithInlineScript)}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	sum := sha256.Sum256([]byte(`console.log("inline bootstrap")`))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, want) {
		t.Fatalf("CSP = %q, want it to contain %q", csp, want)
	}
}

// TestWebUIEveryInlineScriptHasMatchingHash fails if the served index.html
// (the real embedded build) contains an inline <script> whose CSP hash
// isn't present verbatim in the CSP this handler serves - i.e. it would
// fail to run under the CSP a real browser enforces.
func TestWebUIEveryInlineScriptHasMatchingHash(t *testing.T) {
	sub, err := indexSubFS()
	if err != nil {
		t.Fatalf("fs.Sub(distFS, \"dist\"): %v", err)
	}
	data, err := indexBytes(sub)
	if err != nil {
		t.Skipf("no built index.html to check (run task web:build first): %v", err)
	}
	handler := handlerFS(sub)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")

	for _, m := range inlineScriptRe.FindAllSubmatch(data, -1) {
		if strings.Contains(strings.ToLower(string(m[1])), "src=") {
			continue
		}
		sum := sha256.Sum256(m[2])
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(csp, hash) {
			t.Fatalf("inline script has no matching CSP hash: want %q in %q", hash, csp)
		}
	}
}

func TestWebUIServesPlaceholderWhenNotBuilt(t *testing.T) {
	fsys := fstest.MapFS{".gitkeep": {Data: []byte{}}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "task web:build") {
		t.Fatalf("body = %q, want it to mention task web:build", rec.Body.String())
	}
	// No inline script in the placeholder, so no hash is needed.
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sha256-") {
		t.Fatalf("CSP = %q, want no script hash for the placeholder", csp)
	}
}

func TestWebUIHeaders(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("Referrer-Policy = %q, want same-origin", got)
	}
}

func TestWebUIHead(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html>hello</html>")}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
}

func TestWebUIRejectsNonGetHeadOnSPAPath(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/acme/invoices", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want \"GET, HEAD\"", got)
	}
}

func TestWebUIUnmatchedAppPathIs404(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":                 {Data: []byte("<html></html>")},
		"_app/immutable/chunks/a.js": {Data: []byte("console.log(1)")},
	}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_app/immutable/chunks/missing.js", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html>") {
		t.Fatalf("body = %q, want a plain 404, not the SPA shell", rec.Body.String())
	}
}

func TestWebUIDirectoryPathUnderAppIs404(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":                 {Data: []byte("<html></html>")},
		"_app/immutable/chunks/a.js": {Data: []byte("console.log(1)")},
	}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_app/immutable/chunks/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestWebUIDirectoryPathOutsideAppServesSPA(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":  {Data: []byte("<html>shell</html>")},
		"reports/foo": {Data: []byte("x")},
	}
	handler := handlerFS(fsys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/reports/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "shell") {
		t.Fatalf("body = %q, want the SPA shell", rec.Body.String())
	}
}

// indexSubFS returns the real embedded dist/ (not a test fixture), for
// TestWebUIEveryInlineScriptHasMatchingHash, which only makes sense against
// an actual build - it's skipped (via indexBytes's error) against a fresh
// checkout where dist only has .gitkeep.
func indexSubFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

func indexBytes(sub fs.FS) ([]byte, error) {
	return fs.ReadFile(sub, "index.html")
}
