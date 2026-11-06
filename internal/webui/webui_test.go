package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestWebUIServesIndexForDeepLink(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/acme/invoices/42", nil)
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if got := ct; len(got) < len("text/html") || got[:len("text/html")] != "text/html" {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != csp {
		t.Fatalf("CSP = %q, want %q", got, csp)
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
