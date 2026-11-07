// Package config loads shigoto's runtime configuration from environment
// variables, all prefixed SHIGOTO_.
package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Known lists the module names that SHIGOTO_MODULES may select.
var Known = []string{"property", "timesheets", "reconciliation"}

// StripeOrg holds the Stripe credentials for one organization.
type StripeOrg struct {
	SecretKey     string
	WebhookSecret string
}

// OIDC holds the generic OIDC staff-login configuration (Authentik in
// production). A nil *OIDC on Config means SSO is disabled.
type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// SMTP holds the mail delivery configuration. A zero-value Host means email
// is disabled.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// TLS is one of "starttls" (default), "tls" (implicit TLS, e.g. port
	// 465) or "none". "none" is only accepted for a loopback/mailpit host.
	TLS string
}

// Config is shigoto's runtime configuration.
type Config struct {
	Listen      string
	DatabaseURL string
	Modules     map[string]bool
	FilesDir    string
	BaseURL     string
	// Location is the timezone invoice dates (issue_date, due_date) are
	// calculated in, so "today" means the operator's calendar day, not
	// UTC's. Defaults to Australia/Brisbane.
	Location *time.Location
	// SchedulerEnabled controls the in-process hourly scheduler (rent
	// runs and session pruning). Defaults to true.
	SchedulerEnabled bool
	// SMTP holds mail delivery configuration. SMTP.Host == "" means email
	// is disabled.
	SMTP SMTP
	// TrustedProxy is the CIDR (SHIGOTO_TRUSTED_PROXY) whose
	// X-Forwarded-For header is trusted when resolving a request's client
	// IP for login rate limiting. nil means no proxy is trusted: the
	// client IP is always RemoteAddr.
	TrustedProxy *net.IPNet
	// OIDC holds the generic OIDC staff-login configuration. nil means SSO
	// is disabled ("sso": false in GET /api/v1/meta).
	OIDC *OIDC
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
	// The CSRF middleware (internal/httpapi/csrf.go) compares each request's
	// Origin against this origin, so it must parse as an absolute http(s)
	// URL with a host: a malformed value would otherwise only surface as a
	// nil-pointer panic or a silent 403 on every browser write, at request
	// time instead of at startup.
	if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("SHIGOTO_BASE_URL must be an absolute http:// or https:// URL, got %q", c.BaseURL)
	}
	// Normalised once here (no trailing slash) so every consumer that builds
	// a path onto it (OIDC's RedirectURL, invite/magic-link email links) can
	// just concatenate "/..." without each repeating its own
	// strings.TrimRight(c.BaseURL, "/").
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")

	tz := getenv("SHIGOTO_TIMEZONE")
	if tz == "" {
		tz = "Australia/Brisbane"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Config{}, fmt.Errorf("invalid SHIGOTO_TIMEZONE %q: %w", tz, err)
	}
	c.Location = loc

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
		if name == "" {
			continue
		}
		if !isKnownModule(name) {
			return Config{}, fmt.Errorf("unknown module %q (known: %s)", name, strings.Join(Known, ", "))
		}
		c.Modules[name] = true
	}

	scheduler := getenv("SHIGOTO_SCHEDULER")
	switch scheduler {
	case "", "on":
		c.SchedulerEnabled = true
	case "off":
		c.SchedulerEnabled = false
	default:
		return Config{}, fmt.Errorf("invalid SHIGOTO_SCHEDULER %q (want \"on\" or \"off\")", scheduler)
	}

	smtp, err := loadSMTP(getenv)
	if err != nil {
		return Config{}, err
	}
	c.SMTP = smtp

	oidcCfg, err := loadOIDC(getenv)
	if err != nil {
		return Config{}, err
	}
	c.OIDC = oidcCfg

	if tp := getenv("SHIGOTO_TRUSTED_PROXY"); tp != "" {
		_, cidr, err := net.ParseCIDR(tp)
		if err != nil {
			return Config{}, fmt.Errorf("invalid SHIGOTO_TRUSTED_PROXY %q: %w", tp, err)
		}
		c.TrustedProxy = cidr
	}

	return c, nil
}

// loopbackSMTPHosts are the only hosts SHIGOTO_SMTP_TLS=none is accepted
// for: a plaintext loopback or compose-network mail catcher, never a real
// mail server.
var loopbackSMTPHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
	"mailpit":   true,
}

func loadSMTP(getenv func(string) string) (SMTP, error) {
	s := SMTP{
		Host:     getenv("SHIGOTO_SMTP_HOST"),
		Username: getenv("SHIGOTO_SMTP_USERNAME"),
		Password: getenv("SHIGOTO_SMTP_PASSWORD"),
		From:     getenv("SHIGOTO_SMTP_FROM"),
		TLS:      getenv("SHIGOTO_SMTP_TLS"),
	}
	if s.Host == "" {
		// Email disabled; the rest of SMTP's fields are irrelevant.
		return SMTP{}, nil
	}

	if s.TLS == "" {
		s.TLS = "starttls"
	}
	switch s.TLS {
	case "starttls", "tls":
	case "none":
		if !loopbackSMTPHosts[s.Host] {
			return SMTP{}, fmt.Errorf("SHIGOTO_SMTP_TLS=none is only allowed for a loopback or mailpit host, got %q", s.Host)
		}
	default:
		return SMTP{}, fmt.Errorf("invalid SHIGOTO_SMTP_TLS %q (want \"starttls\", \"tls\" or \"none\")", s.TLS)
	}

	if s.From == "" {
		return SMTP{}, fmt.Errorf("SHIGOTO_SMTP_FROM is required when SHIGOTO_SMTP_HOST is set")
	}

	portStr := getenv("SHIGOTO_SMTP_PORT")
	if portStr == "" {
		switch s.TLS {
		case "tls":
			s.Port = 465
		case "none":
			s.Port = 1025
		default:
			s.Port = 587
		}
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return SMTP{}, fmt.Errorf("invalid SHIGOTO_SMTP_PORT %q", portStr)
		}
		s.Port = port
	}

	return s, nil
}

// loadOIDC reads SHIGOTO_OIDC_ISSUER, SHIGOTO_OIDC_CLIENT_ID and
// SHIGOTO_OIDC_CLIENT_SECRET. All three are required together, or none of
// them (a partially-configured IdP would otherwise fail confusingly deep
// inside the first login attempt instead of at startup). nil means SSO is
// disabled.
func loadOIDC(getenv func(string) string) (*OIDC, error) {
	issuer := getenv("SHIGOTO_OIDC_ISSUER")
	clientID := getenv("SHIGOTO_OIDC_CLIENT_ID")
	clientSecret := getenv("SHIGOTO_OIDC_CLIENT_SECRET")

	set := 0
	for _, v := range []string{issuer, clientID, clientSecret} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return nil, nil
	}
	if set != 3 {
		return nil, fmt.Errorf("SHIGOTO_OIDC_ISSUER, SHIGOTO_OIDC_CLIENT_ID and SHIGOTO_OIDC_CLIENT_SECRET must all be set together, or none of them")
	}

	scopes := []string{"openid", "email", "profile"}
	if raw := strings.TrimSpace(getenv("SHIGOTO_OIDC_SCOPES")); raw != "" {
		scopes = strings.Fields(raw)
	}

	return &OIDC{Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret, Scopes: scopes}, nil
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
