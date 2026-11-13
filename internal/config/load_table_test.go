package config

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"shigoto/internal/testkit/table"
)

type loadCase struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Want *loadWant         `yaml:"want"`
	Err  string            `yaml:"err"`
}

// loadWant lists the Config fields a case may check. A nil field is not
// checked.
type loadWant struct {
	Listen         *string  `yaml:"listen"`
	BaseURL        *string  `yaml:"base_url"`
	Modules        []string `yaml:"modules"` // the exact set of enabled modules
	Timezone       *string  `yaml:"timezone"`
	Scheduler      *bool    `yaml:"scheduler"`
	SMTPHost       *string  `yaml:"smtp_host"`
	SMTPTLS        *string  `yaml:"smtp_tls"`
	TrustedProxy   *string  `yaml:"trusted_proxy"` // "" means no proxy is trusted
	OIDCConfigured *bool    `yaml:"oidc_configured"`
	OIDCScopes     []string `yaml:"oidc_scopes"`
}

func TestLoadTable(t *testing.T) {
	const path = "testdata/load.yaml"
	for _, c := range table.Load[loadCase](t, path) {
		t.Run(c.Name, func(t *testing.T) {
			if (c.Want == nil) == (c.Err == "") {
				t.Fatalf("%s: %q: needs exactly one of want or err", path, c.Name)
			}
			vars := map[string]string{
				"SHIGOTO_DATABASE_URL": "postgres://x",
				"SHIGOTO_BASE_URL":     "http://localhost:8080",
			}
			for k, v := range c.Env {
				vars[k] = v
			}
			got, err := Load(env(vars))
			if c.Err != "" {
				if err == nil || !strings.Contains(err.Error(), c.Err) {
					t.Fatalf("%s: %q: error = %v, want it to mention %q", path, c.Name, err, c.Err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %q: unexpected error: %v", path, c.Name, err)
			}
			c.Want.check(t, path+": "+c.Name, got)
		})
	}
}

func (w *loadWant) check(t *testing.T, label string, c Config) {
	t.Helper()
	eq := func(field string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %s = %v, want %v", label, field, got, want)
		}
	}
	if w.Listen != nil {
		eq("Listen", c.Listen, *w.Listen)
	}
	if w.BaseURL != nil {
		eq("BaseURL", c.BaseURL, *w.BaseURL)
	}
	if w.Modules != nil {
		var enabled []string
		for name, on := range c.Modules {
			if on {
				enabled = append(enabled, name)
			}
		}
		want := slices.Clone(w.Modules)
		sort.Strings(enabled)
		sort.Strings(want)
		if !slices.Equal(enabled, want) || len(c.Modules) != len(want) {
			t.Errorf("%s: Modules = %v, want exactly %v enabled", label, c.Modules, want)
		}
	}
	if w.Timezone != nil {
		if c.Location == nil {
			t.Errorf("%s: Location = nil, want %s", label, *w.Timezone)
		} else {
			eq("Location", c.Location.String(), *w.Timezone)
		}
	}
	if w.Scheduler != nil {
		eq("SchedulerEnabled", c.SchedulerEnabled, *w.Scheduler)
	}
	if w.SMTPHost != nil {
		eq("SMTP.Host", c.SMTP.Host, *w.SMTPHost)
	}
	if w.SMTPTLS != nil {
		eq("SMTP.TLS", c.SMTP.TLS, *w.SMTPTLS)
	}
	if w.TrustedProxy != nil {
		got := ""
		if c.TrustedProxy != nil {
			got = c.TrustedProxy.String()
		}
		eq("TrustedProxy", got, *w.TrustedProxy)
	}
	if w.OIDCConfigured != nil {
		eq("OIDC configured", c.OIDC != nil, *w.OIDCConfigured)
	}
	if w.OIDCScopes != nil {
		if c.OIDC == nil || !slices.Equal(c.OIDC.Scopes, w.OIDCScopes) {
			t.Errorf("%s: OIDC scopes = %+v, want %v", label, c.OIDC, w.OIDCScopes)
		}
	}
}
