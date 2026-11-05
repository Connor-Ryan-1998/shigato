package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"shigoto/internal/config"
	"shigoto/internal/core"
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

func TestReadJSONRejectsUnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"known":"a","extra":"b"}`))
	var body struct {
		Known string `json:"known"`
	}
	if err := readJSON(req, &body); err == nil {
		t.Fatal("readJSON with unknown field: got nil error, want error")
	}
}

// TestInvoicePayOptionsOmitStripeWithoutURL covers finding 5: once an
// invoice is issued, stripe is only a real option if it has a provider
// URL to pay at.
func TestInvoicePayOptionsOmitStripeWithoutURL(t *testing.T) {
	opts := []string{"bank_transfer", "payid", "stripe"}
	cases := []struct {
		name string
		inv  core.Invoice
		want []string
	}{
		{"draft", core.Invoice{Status: "draft"}, opts},
		{"open, sent", core.Invoice{Status: "open", ProviderURL: "https://pay.example/1"}, opts},
		{"open, unsent", core.Invoice{Status: "open"}, []string{"bank_transfer", "payid"}},
	}
	for _, c := range cases {
		if got := invoicePayOptions(c.inv, opts); !slices.Equal(got, c.want) {
			t.Errorf("%s: pay options = %v, want %v", c.name, got, c.want)
		}
	}
}
