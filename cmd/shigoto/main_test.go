package main

import (
	"strings"
	"testing"
)

// TestCreateOwnerRejectsBadSlug checks the slug is validated before any
// password prompt or database work: slugs appear in URLs and env var
// names (SHIGOTO_STRIPE_<SLUG>_...).
func TestCreateOwnerRejectsBadSlug(t *testing.T) {
	for _, slug := range []string{"Acme", "acme co", "acme_co", "acme/x"} {
		err := adminCreateOwner([]string{"--org-slug", slug, "--org-name", "Acme", "--email", "a@example.com"})
		if err == nil || !strings.Contains(err.Error(), "org-slug") {
			t.Errorf("slug %q: err = %v, want an org-slug validation error", slug, err)
		}
	}
}
