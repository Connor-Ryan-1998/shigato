package config

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Listen != ":8080" {
		t.Errorf("Listen = %q, want %q", c.Listen, ":8080")
	}
	if !c.Modules["property"] || !c.Modules["timesheets"] || !c.Modules["reconciliation"] {
		t.Errorf("Modules = %+v, want all known modules true", c.Modules)
	}
}

func TestLoadRejectsUnknownModule(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_MODULES":      "property,payroll",
	}))
	if err == nil {
		t.Fatal("expected error for unknown module, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "payroll") {
		t.Errorf("error = %q, want it to mention %q", got, "payroll")
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_BASE_URL": "http://localhost:8080",
	}))
	if err == nil {
		t.Fatal("expected error for missing database URL, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "SHIGOTO_DATABASE_URL") {
		t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_DATABASE_URL")
	}
}

func TestLoadRequiresBaseURL(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
	}))
	if err == nil {
		t.Fatal("expected error for missing base URL, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "SHIGOTO_BASE_URL") {
		t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_BASE_URL")
	}
}

func TestModulesSubset(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_MODULES":      "timesheets",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Modules["property"] || c.Modules["reconciliation"] {
		t.Errorf("Modules = %+v, want only timesheets true", c.Modules)
	}
	if !c.Modules["timesheets"] {
		t.Errorf("Modules = %+v, want timesheets true", c.Modules)
	}
}

func TestStripeFor(t *testing.T) {
	getenv := env(map[string]string{
		"SHIGOTO_DATABASE_URL":                 "postgres://x",
		"SHIGOTO_BASE_URL":                     "http://localhost:8080",
		"SHIGOTO_STRIPE_MY_ORG_SECRET_KEY":     "sk_test_123",
		"SHIGOTO_STRIPE_MY_ORG_WEBHOOK_SECRET": "whsec_123",
	})

	org, ok := StripeFor(getenv, "my-org")
	if !ok {
		t.Fatal("expected ok=true for configured org")
	}
	if org.SecretKey != "sk_test_123" || org.WebhookSecret != "whsec_123" {
		t.Errorf("org = %+v, want secret/webhook from env", org)
	}

	_, ok = StripeFor(getenv, "other-org")
	if ok {
		t.Error("expected ok=false for unconfigured org")
	}
}
