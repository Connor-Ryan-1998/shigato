//go:build integration

// Package testkit is the shared integration-test harness: a migrated test
// database, org/user/membership builders, real sessions against the full
// HTTP handler, and table snapshots for asserting what a request changed.
package testkit

import (
	"context"
	"maps"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/config"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/mail"
	"shigoto/internal/payments"
)

// Env is one test's isolated database plus the context its helpers use.
// T is exported so a test can swap in a recording testing.TB.
type Env struct {
	T    testing.TB
	Pool *pgxpool.Pool
	Ctx  context.Context
}

// New returns an Env backed by a fresh, migrated schema that is dropped when
// t finishes. It skips t when SHIGOTO_TEST_DATABASE_URL is unset.
func New(t testing.TB) *Env {
	t.Helper()
	return &Env{T: t, Pool: db.TestPool(t), Ctx: context.Background()}
}

type handlerConfig struct {
	modules     map[string]bool
	mailer      *mail.Mailer
	payments    func(string) (payments.Provider, error)
	billing     *billing.Service
	authOptions []auth.Option
}

// HandlerOption customises the handler Env.Handler builds.
type HandlerOption func(*handlerConfig)

// Modules overrides individual module switches. Modules not named keep their
// default (enabled), so Modules(map[string]bool{"property": false}) disables
// only property.
func Modules(m map[string]bool) HandlerOption {
	return func(c *handlerConfig) { maps.Copy(c.modules, m) }
}

// Mailer sets the handler's mailer. The default is nil (no email).
func Mailer(m *mail.Mailer) HandlerOption {
	return func(c *handlerConfig) { c.mailer = m }
}

// Payments sets the handler's payments-provider resolver.
func Payments(p func(string) (payments.Provider, error)) HandlerOption {
	return func(c *handlerConfig) { c.payments = p }
}

// Billing sets the handler's billing service.
func Billing(b *billing.Service) HandlerOption {
	return func(c *handlerConfig) { c.billing = b }
}

// AuthOptions passes options to auth.New.
func AuthOptions(opts ...auth.Option) HandlerOption {
	return func(c *handlerConfig) { c.authOptions = append(c.authOptions, opts...) }
}

// Handler builds the full production HTTP handler over e's database. By
// default every config.Known module is enabled, GetEnv returns "" and auth
// uses insecure (non-Secure) cookies.
func (e *Env) Handler(opts ...HandlerOption) http.Handler {
	c := handlerConfig{modules: make(map[string]bool, len(config.Known))}
	for _, name := range config.Known {
		c.modules[name] = true
	}
	for _, opt := range opts {
		opt(&c)
	}
	return httpapi.New(httpapi.Deps{
		Pool:     e.Pool,
		Auth:     auth.New(e.Pool, false, c.authOptions...),
		Cfg:      config.Config{Modules: c.modules, BaseURL: "https://shigoto.example"},
		GetEnv:   func(string) string { return "" },
		Payments: c.payments,
		Billing:  c.billing,
		Mailer:   c.mailer,
	})
}
