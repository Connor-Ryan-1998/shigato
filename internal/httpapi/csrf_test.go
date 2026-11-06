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
	handler := csrfMiddleware("https://app.example.com", next)

	tests := []struct {
		name       string
		method     string
		path       string
		csrfHeader string
		origin     string
		wantStatus int
	}{
		{"POST without header", http.MethodPost, "/api/v1/x", "", "", http.StatusForbidden},
		{"POST with header passes", http.MethodPost, "/api/v1/x", "1", "", http.StatusOK},
		{"POST with header and foreign origin", http.MethodPost, "/api/v1/x", "1", "https://evil.example.com", http.StatusForbidden},
		{"POST with header and matching origin", http.MethodPost, "/api/v1/x", "1", "https://app.example.com", http.StatusOK},
		{"POST with header and null origin", http.MethodPost, "/api/v1/x", "1", "null", http.StatusForbidden},
		{"POST with header and malformed origin", http.MethodPost, "/api/v1/x", "1", "http://[", http.StatusForbidden},
		{"GET without header passes", http.MethodGet, "/api/v1/x", "", "", http.StatusOK},
		{"POST to webhook without header passes", http.MethodPost, "/webhooks/stripe/x", "", "", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
