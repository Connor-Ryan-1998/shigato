// Package config loads shigoto's runtime configuration from environment
// variables, all prefixed SHIGOTO_.
package config

import (
	"fmt"
	"strings"
)

// Known lists the module names that SHIGOTO_MODULES may select.
var Known = []string{"property", "timesheets", "reconciliation"}

// StripeOrg holds the Stripe credentials for one organization.
type StripeOrg struct {
	SecretKey     string
	WebhookSecret string
}

// Config is shigoto's runtime configuration.
type Config struct {
	Listen      string
	DatabaseURL string
	Modules     map[string]bool
	FilesDir    string
	BaseURL     string
	Stripe      map[string]StripeOrg
}

// Load reads configuration from environment variables via getenv.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:   getenv("SHIGOTO_LISTEN"),
		FilesDir: getenv("SHIGOTO_FILES_DIR"),
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.FilesDir == "" {
		c.FilesDir = "./data/files"
	}

	c.DatabaseURL = getenv("SHIGOTO_DATABASE_URL")
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("SHIGOTO_DATABASE_URL is required")
	}

	c.BaseURL = getenv("SHIGOTO_BASE_URL")
	if c.BaseURL == "" {
		return Config{}, fmt.Errorf("SHIGOTO_BASE_URL is required")
	}

	modules := strings.TrimSpace(getenv("SHIGOTO_MODULES"))
	var names []string
	if modules == "" {
		names = Known
	} else {
		names = strings.Split(modules, ",")
	}
	c.Modules = make(map[string]bool, len(Known))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if !isKnownModule(name) {
			return Config{}, fmt.Errorf("unknown module %q (known: %s)", name, strings.Join(Known, ", "))
		}
		c.Modules[name] = true
	}

	return c, nil
}

func isKnownModule(name string) bool {
	for _, k := range Known {
		if k == name {
			return true
		}
	}
	return false
}

// StripeFor reads the Stripe credentials for org slug from environment
// variables SHIGOTO_STRIPE_<SLUG>_SECRET_KEY and
// SHIGOTO_STRIPE_<SLUG>_WEBHOOK_SECRET, where SLUG is the org slug upper
// cased with '-' replaced by '_'. ok is false if neither variable is set.
func StripeFor(getenv func(string) string, slug string) (StripeOrg, bool) {
	key := strings.ToUpper(strings.ReplaceAll(slug, "-", "_"))
	secretKey := getenv("SHIGOTO_STRIPE_" + key + "_SECRET_KEY")
	webhookSecret := getenv("SHIGOTO_STRIPE_" + key + "_WEBHOOK_SECRET")
	if secretKey == "" && webhookSecret == "" {
		return StripeOrg{}, false
	}
	return StripeOrg{SecretKey: secretKey, WebhookSecret: webhookSecret}, true
}
