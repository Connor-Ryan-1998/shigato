package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRF(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		baseURL    string
		method     string
		path       string
		csrfHeader string
		origin     string
		wantStatus int
	}{
		{"POST without header", "https://app.example.com", http.MethodPost, "/api/v1/x", "", "", http.StatusForbidden},
		{"POST with header passes", "https://app.example.com", http.MethodPost, "/api/v1/x", "1", "", http.StatusOK},
		{"POST with header and foreign origin", "https://app.example.com", http.MethodPost, "/api/v1/x", "1", "https://evil.example.com", http.StatusForbidden},
		{"POST with header and matching origin", "https://app.example.com", http.MethodPost, "/api/v1/x", "1", "https://app.example.com", http.StatusOK},
		{"POST with header and null origin", "https://app.example.com", http.MethodPost, "/api/v1/x", "1", "null", http.StatusForbidden},
		{"POST with header and malformed origin", "https://app.example.com", http.MethodPost, "/api/v1/x", "1", "http://[", http.StatusForbidden},
		{"GET without header passes", "https://app.example.com", http.MethodGet, "/api/v1/x", "", "", http.StatusOK},
		{"HEAD without header passes", "https://app.example.com", http.MethodHead, "/api/v1/x", "", "", http.StatusOK},
		{"POST to webhook without header passes", "https://app.example.com", http.MethodPost, "/webhooks/stripe/x", "", "", http.StatusOK},
		{"PUT without header", "https://app.example.com", http.MethodPut, "/api/v1/x", "", "", http.StatusForbidden},
		{"PATCH without header", "https://app.example.com", http.MethodPatch, "/api/v1/x", "", "", http.StatusForbidden},
		{"DELETE without header", "https://app.example.com", http.MethodDelete, "/api/v1/x", "", "", http.StatusForbidden},
		{"lowercase method without header", "https://app.example.com", "post", "/api/v1/x", "", "", http.StatusForbidden},
		// Origin comparison is case-insensitive on host and strips the
		// scheme's default port, since both are legal variations a real
		// browser or operator-typed BaseURL can produce.
		{"uppercase host in BaseURL matches lowercase origin", "https://App.Example.com", http.MethodPost, "/api/v1/x", "1", "https://app.example.com", http.StatusOK},
		{"explicit default port in BaseURL matches no-port origin", "https://app.example.com:443", http.MethodPost, "/api/v1/x", "1", "https://app.example.com", http.StatusOK},
		{"scheme mismatch is rejected even with matching host", "https://app.example.com", http.MethodPost, "/api/v1/x", "1", "http://app.example.com", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := csrfMiddleware(tt.baseURL, next)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.csrfHeader != "" {
				req.Header.Set("X-Shigoto-CSRF", tt.csrfHeader)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestCSRFMalformedBaseURLFailsSafe checks that an invalid BaseURL (which
// config.Load rejects before this middleware ever sees it in production)
// doesn't panic the middleware: it should just reject every request that
// carries an Origin, since there's no valid origin to compare against.
func TestCSRFMalformedBaseURLFailsSafe(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := csrfMiddleware("http://[", next)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/x", nil)
	req.Header.Set("X-Shigoto-CSRF", "1")
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// No Origin header: nothing to compare, so it still passes through.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/x", nil)
	req2.Header.Set("X-Shigoto-CSRF", "1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec2.Code, http.StatusOK)
	}
}
