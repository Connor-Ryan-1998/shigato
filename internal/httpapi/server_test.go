package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shigoto/internal/config"
	"shigoto/internal/module"
	"shigoto/internal/payments"
)

func TestMetaListsEnabledModules(t *testing.T) {
	saved := modules
	defer func() { modules = saved }()

	modules = []module.Module{
		{
			Name: "property",
			Routes: func(mux *http.ServeMux, d module.Deps) {
				mux.HandleFunc("GET /api/v1/property/ping", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				})
			},
		},
	}

	d := Deps{Cfg: config.Config{Modules: map[string]bool{"property": false}}}
	handler := New(d)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/property/ping", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled module route: got status %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	handler.ServeHTTP(rec, req)
	assertMeta(t, rec, []string{})

	d = Deps{Cfg: config.Config{Modules: map[string]bool{"property": true}}}
	handler = New(d)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/property/ping", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled module route: got status %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	handler.ServeHTTP(rec, req)
	assertMeta(t, rec, []string{"property"})
}

func assertMeta(t *testing.T, rec *httptest.ResponseRecorder, want []string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/meta: got status %d, want 200", rec.Code)
	}
	var body struct {
		Modules []string `json:"modules"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding meta response: %v", err)
	}
	if len(body.Modules) != len(want) {
		t.Fatalf("meta modules = %v, want %v", body.Modules, want)
	}
	for i := range want {
		if body.Modules[i] != want[i] {
			t.Fatalf("meta modules = %v, want %v", body.Modules, want)
		}
	}
}

// TestDefaultPaymentsRequiresBothStripeSecrets checks the default Payments
// resolver (used when Deps.Payments is nil) never hands back a Stripe
// provider unless both the secret key and webhook secret are configured: a
// secret key alone could create charges but can't verify the webhooks that
// confirm they were paid, so an invoice must never be pushed to a Stripe
// account shigoto can't hear back from.
func TestDefaultPaymentsRequiresBothStripeSecrets(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	resolve := defaultPayments(getenv, time.UTC)

	if _, err := resolve("acme"); !errors.Is(err, payments.ErrNoProvider) {
		t.Fatalf("no keys: err = %v, want ErrNoProvider", err)
	}

	env["SHIGOTO_STRIPE_ACME_SECRET_KEY"] = "sk_test_dummy"
	if _, err := resolve("acme"); !errors.Is(err, payments.ErrNoProvider) {
		t.Fatalf("secret key only: err = %v, want ErrNoProvider", err)
	}

	env["SHIGOTO_STRIPE_ACME_WEBHOOK_SECRET"] = "whsec_test_dummy"
	p, err := resolve("acme")
	if err != nil {
		t.Fatalf("both keys: err = %v, want nil", err)
	}
	if p == nil {
		t.Fatal("both keys: provider = nil, want non-nil")
	}
}

// TestUnknownAPIPathIsJSON404 checks that an /api/ path no route owns comes
// back as this API's {"error": "..."} JSON, not net/http.ServeMux's
// plain-text 404 - and critically, not the SPA's index.html, which an
// unguarded "/" catch-all would otherwise serve for it (see routeWebUI).
func TestUnknownAPIPathIsJSON404(t *testing.T) {
	d := Deps{Cfg: config.Config{Modules: map[string]bool{}}}
	handler := New(d)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Error == "" {
		t.Fatal("error message is empty")
	}
}

// TestKnownAPIPathWrongMethodIsJSON405 checks the wrong method on a path
// that does exist (GET /api/v1/meta) also gets this API's JSON error body,
// not net/http.ServeMux's plain-text 405.
func TestKnownAPIPathWrongMethodIsJSON405(t *testing.T) {
	d := Deps{Cfg: config.Config{Modules: map[string]bool{}}}
	handler := New(d)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meta", nil)
	req.Header.Set("X-Shigoto-CSRF", "1")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Error == "" {
		t.Fatal("error message is empty")
	}
}

func TestReadJSONRejectsUnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"known":"a","extra":"b"}`))
	var body struct {
		Known string `json:"known"`
	}
	if err := readJSON(req, &body); err == nil {
		t.Fatal("readJSON with unknown field: got nil error, want error")
	}
}
