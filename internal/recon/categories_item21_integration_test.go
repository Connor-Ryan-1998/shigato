//go:build integration

package recon_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/testkit"
)

func TestNewOrgCategoriesCarryItem21Labels(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	if got := categoryByName(t, e.Pool, org.ID, "Council rates").Item21Label; got == nil || *got != core.Item21CouncilRates {
		t.Errorf("Council rates label = %v, want council_rates", got)
	}
	if got := categoryByName(t, e.Pool, org.ID, "Bank fees").Item21Label; got == nil || *got != core.Item21Sundry {
		t.Errorf("Bank fees label = %v, want sundry", got)
	}
	if got := categoryByName(t, e.Pool, org.ID, "Equipment").Item21Label; got != nil {
		t.Errorf("Equipment label = %v, want nil", *got)
	}
}

func TestCategoryItem21LabelUpdate(t *testing.T) {
	e := testkit.New(t)
	org, s := e.OwnerSession("acme", e.Handler())
	water := categoryByName(t, e.Pool, org.ID, "Other expenses")
	path := fmt.Sprintf("/api/v1/orgs/acme/categories/%d", water.ID)
	body := func(label any) map[string]any {
		return map[string]any{"name": "Other expenses", "type": "expense", "default_gst_code": "GST", "capital": false, "item21_label": label}
	}

	for _, bad := range []string{"capital_works", "capital_allowances", "nonsense"} {
		if rr := s.Do(http.MethodPut, path, body(bad)); rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("label %s: status %d, want 422", bad, rr.Code)
		}
	}
	rr := s.Do(http.MethodPut, path, body("gardening"))
	if rr.Code != http.StatusOK || !contains(rr.Body, `"item21_label":"gardening"`) {
		t.Fatalf("set: %d %s", rr.Code, rr.Body)
	}
	rr = s.Do(http.MethodPut, path, body(nil))
	if rr.Code != http.StatusOK || !contains(rr.Body, `"item21_label":null`) {
		t.Fatalf("clear: %d %s", rr.Code, rr.Body)
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
