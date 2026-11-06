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

func TestLoadRejectsInvalidBaseURL(t *testing.T) {
	cases := []string{
		"not-a-url",
		"ftp://example.com",
		"http://",
		"https:// has a space",
	}
	for _, baseURL := range cases {
		t.Run(baseURL, func(t *testing.T) {
			_, err := Load(env(map[string]string{
				"SHIGOTO_DATABASE_URL": "postgres://x",
				"SHIGOTO_BASE_URL":     baseURL,
			}))
			if err == nil {
				t.Fatalf("expected error for invalid SHIGOTO_BASE_URL %q, got nil", baseURL)
			}
			if got := err.Error(); !strings.Contains(got, "SHIGOTO_BASE_URL") {
				t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_BASE_URL")
			}
		})
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

func TestLoadDefaultTimezone(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Location == nil || c.Location.String() != "Australia/Brisbane" {
		t.Errorf("Location = %v, want Australia/Brisbane", c.Location)
	}
}

func TestLoadRejectsInvalidTimezone(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_TIMEZONE":     "Not/AZone",
	}))
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "Not/AZone") {
		t.Errorf("error = %q, want it to mention %q", got, "Not/AZone")
	}
}

func TestLoadSchedulerDefaultsOn(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.SchedulerEnabled {
		t.Errorf("SchedulerEnabled = false, want true")
	}
}

func TestLoadSchedulerOff(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_SCHEDULER":    "off",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.SchedulerEnabled {
		t.Errorf("SchedulerEnabled = true, want false")
	}
}

func TestLoadRejectsInvalidScheduler(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_SCHEDULER":    "maybe",
	}))
	if err == nil {
		t.Fatal("expected error for invalid SHIGOTO_SCHEDULER, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "SHIGOTO_SCHEDULER") {
		t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_SCHEDULER")
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

func TestLoadSMTPDisabledByDefault(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.SMTP.Host != "" {
		t.Errorf("SMTP.Host = %q, want empty (email disabled)", c.SMTP.Host)
	}
}

func TestLoadSMTPDefaultTLSIsStarttls(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_SMTP_HOST":    "smtp.example.com",
		"SHIGOTO_SMTP_FROM":    "noreply@example.com",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.SMTP.TLS != "starttls" {
		t.Errorf("SMTP.TLS = %q, want %q", c.SMTP.TLS, "starttls")
	}
}

func TestLoadSMTPNoneRejectedForRealHost(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_SMTP_HOST":    "smtp.example.com",
		"SHIGOTO_SMTP_FROM":    "noreply@example.com",
		"SHIGOTO_SMTP_TLS":     "none",
	}))
	if err == nil {
		t.Fatal("expected error for SHIGOTO_SMTP_TLS=none with a non-loopback host, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "SHIGOTO_SMTP_TLS") {
		t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_SMTP_TLS")
	}
}

func TestLoadSMTPNoneAllowedForMailpit(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_SMTP_HOST":    "mailpit",
		"SHIGOTO_SMTP_FROM":    "noreply@example.com",
		"SHIGOTO_SMTP_TLS":     "none",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.SMTP.TLS != "none" {
		t.Errorf("SMTP.TLS = %q, want %q", c.SMTP.TLS, "none")
	}
}

func TestLoadTrustedProxyUnsetByDefault(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.TrustedProxy != nil {
		t.Errorf("TrustedProxy = %v, want nil", c.TrustedProxy)
	}
}

func TestLoadTrustedProxyParsesCIDR(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL":  "postgres://x",
		"SHIGOTO_BASE_URL":      "http://localhost:8080",
		"SHIGOTO_TRUSTED_PROXY": "10.0.0.0/8",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.TrustedProxy == nil || c.TrustedProxy.String() != "10.0.0.0/8" {
		t.Errorf("TrustedProxy = %v, want 10.0.0.0/8", c.TrustedProxy)
	}
}

func TestLoadTrustedProxyRejectsInvalidCIDR(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL":  "postgres://x",
		"SHIGOTO_BASE_URL":      "http://localhost:8080",
		"SHIGOTO_TRUSTED_PROXY": "not-a-cidr",
	}))
	if err == nil {
		t.Fatal("expected error for invalid SHIGOTO_TRUSTED_PROXY, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "SHIGOTO_TRUSTED_PROXY") {
		t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_TRUSTED_PROXY")
	}
}

func TestLoadSMTPMissingFromIsError(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SHIGOTO_DATABASE_URL": "postgres://x",
		"SHIGOTO_BASE_URL":     "http://localhost:8080",
		"SHIGOTO_SMTP_HOST":    "smtp.example.com",
	}))
	if err == nil {
		t.Fatal("expected error for missing SHIGOTO_SMTP_FROM, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "SHIGOTO_SMTP_FROM") {
		t.Errorf("error = %q, want it to mention %q", got, "SHIGOTO_SMTP_FROM")
	}
}
