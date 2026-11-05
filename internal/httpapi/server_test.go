package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shigoto/internal/config"
)

func TestMetaListsEnabledModules(t *testing.T) {
	saved := modules
	defer func() { modules = saved }()

	modules = []Module{
		{
			Name: "property",
			Routes: func(mux *http.ServeMux, d Deps) {
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

func TestReadJSONRejectsUnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"known":"a","extra":"b"}`))
	var body struct {
		Known string `json:"known"`
	}
	if err := readJSON(req, &body); err == nil {
		t.Fatal("readJSON with unknown field: got nil error, want error")
	}
}
