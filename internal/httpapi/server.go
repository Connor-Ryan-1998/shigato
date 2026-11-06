package httpapi

import (
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/config"
	"shigoto/internal/module"
	"shigoto/internal/payments"
	"shigoto/internal/payments/stripe"
	"shigoto/internal/property"
	"shigoto/internal/recon"
	"shigoto/internal/reports"
	"shigoto/internal/timesheets"
)

// Deps holds httpapi's own dependencies, a superset of module.Deps: it adds
// the bits (GetEnv, Payments) only core routes and module construction need,
// which modules themselves don't see.
type Deps struct {
	Pool *pgxpool.Pool
	Auth *auth.Auth
	Cfg  config.Config
	// GetEnv reads environment variables, e.g. for config.StripeFor. It
	// defaults to os.Getenv so tests can inject a fake and production
	// code doesn't need to set it. Threading it through here (rather
	// than handlers calling os.Getenv directly) keeps handlers testable.
	GetEnv func(string) string
	// Payments resolves the payments.Provider for an org slug. It
	// defaults to building a stripe.Provider from config.StripeFor,
	// returning payments.ErrNoProvider when the org has no Stripe
	// credentials. Tests inject a func returning a *payments.Fake.
	Payments func(orgSlug string) (payments.Provider, error)
	// Billing issues, sends and voids invoices. It defaults to a
	// *billing.Service built from Pool, GetEnv, Payments and
	// Cfg.Location.
	Billing *billing.Service
}

// defaultPayments builds the default Payments resolver: a stripe.Provider
// from config.StripeFor, in loc. It requires both a secret key and a
// webhook secret before returning a provider - a secret key alone can
// create charges but can't verify the webhooks that confirm they were
// paid, so an invoice must never be pushed to a Stripe account shigoto
// can't hear back from. Otherwise it returns payments.ErrNoProvider.
func defaultPayments(getenv func(string) string, loc *time.Location) func(orgSlug string) (payments.Provider, error) {
	return func(orgSlug string) (payments.Provider, error) {
		org, ok := config.StripeFor(getenv, orgSlug)
		if !ok || org.SecretKey == "" || org.WebhookSecret == "" {
			return nil, payments.ErrNoProvider
		}
		return stripe.New(org.SecretKey, org.WebhookSecret, loc), nil
	}
}

// modules lists the feature modules available to mount. Plans 2, 3 and 4
// each append one entry. It is a package-level var (not a const slice) so
// tests can swap it out and restore it.
var modules = []module.Module{property.Module(), timesheets.Module(), recon.Module()}

// withDefaults fills in d's optional fields the way New always has:
// GetEnv, Cfg.Location, Payments and Billing.
func (d Deps) withDefaults() Deps {
	if d.GetEnv == nil {
		d.GetEnv = os.Getenv
	}
	if d.Cfg.Location == nil {
		// Callers that build Config by hand (mainly tests) instead of
		// config.Load may leave this nil; fall back to UTC rather than
		// let time.Now().In(nil) panic in the invoice-issuing handler.
		d.Cfg.Location = time.UTC
	}
	if d.Payments == nil {
		d.Payments = defaultPayments(d.GetEnv, d.Cfg.Location)
	}
	if d.Billing == nil {
		d.Billing = billing.New(d.Pool, d.GetEnv, d.Payments, d.Cfg.Location)
	}
	return d
}

// DefaultBilling returns the billing.Service New would build from d by
// default. main.go calls this once and shares the result between New and
// the scheduler, so the two don't end up with divergently-built services.
func DefaultBilling(d Deps) *billing.Service {
	return d.withDefaults().Billing
}

// New builds the HTTP handler: the core routes (auth, health, meta) plus
// each module in modules whose name is enabled in d.Cfg.Modules.
func New(d Deps) http.Handler {
	d = d.withDefaults()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		d.Auth.LoginHandler(w, r)
	})
	mux.HandleFunc("POST /api/v1/auth/logout", d.Auth.LogoutHandler)

	registerCoreRoutes(mux, d)
	registerSettingsRoutes(mux, d)
	registerWebhookRoutes(mux, d)
	registerMeRoutes(mux, d)
	registerDashboardRoutes(mux, d)

	md := module.Deps{Pool: d.Pool, Auth: d.Auth, Cfg: d.Cfg, Billing: d.Billing}
	// Reports are core: always on, never gated by d.Cfg.Modules.
	reports.RegisterRoutes(mux, md)
	var enabled []string
	for _, m := range modules {
		if d.Cfg.Modules[m.Name] {
			enabled = append(enabled, m.Name)
			m.Routes(mux, md)
		}
	}
	sort.Strings(enabled)
	if enabled == nil {
		enabled = []string{}
	}

	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"modules": enabled})
	})

	return csrfMiddleware(d.Cfg.BaseURL, mux)
}
