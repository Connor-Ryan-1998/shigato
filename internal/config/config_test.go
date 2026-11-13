package config

import "testing"

func env(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
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
