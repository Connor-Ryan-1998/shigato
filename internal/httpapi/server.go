package httpapi

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/config"
	"shigoto/internal/mail"
	"shigoto/internal/module"
	"shigoto/internal/payments"
	"shigoto/internal/payments/stripe"
	"shigoto/internal/property"
	"shigoto/internal/recon"
	"shigoto/internal/reports"
	"shigoto/internal/timesheets"
	"shigoto/internal/webui"
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
	// Mailer sends invite emails. It defaults to mail.New(Cfg.SMTP), which
	// is nil when SMTP isn't configured - callers (e.g. createInvite) must
	// handle a nil Mailer by skipping the send, not erroring: the invite
	// link is still returned to the caller so it can be shared by hand.
	Mailer *mail.Mailer
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
	if d.Mailer == nil {
		d.Mailer = mail.New(d.Cfg.SMTP)
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
	mux.HandleFunc("GET /api/v1/auth/oidc/start", d.Auth.OIDCStartHandler)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", d.Auth.OIDCCallbackHandler)

	registerCoreRoutes(mux, d)
	registerSettingsRoutes(mux, d)
	registerWebhookRoutes(mux, d)
	registerMeRoutes(mux, d)
	registerDashboardRoutes(mux, d)
	registerMemberRoutes(mux, d)

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
		writeJSON(w, http.StatusOK, map[string]any{"modules": enabled, "sso": d.Auth.OIDCEnabled()})
	})

	return csrfMiddleware(d.Cfg.BaseURL, routeWebUI(mux))
}

// routeWebUI wires in the embedded SPA (internal/webui) as the catch-all for
// everything mux doesn't own: it only ever registers /healthz and specific
// /api/ and /webhooks/ paths, never a "/" pattern, so an unmatched /api/
// path or a wrong method on a known one would otherwise fall through to
// mux's own un-routed 404/405 - not to the SPA, since those two prefixes
// are routed to mux here, never to the webui handler.
//
// /api/ requests additionally go through jsonAPIFallback, because mux's own
// 404 ("404 page not found") and 405 ("Method Not Allowed") responses are
// plain text, not the {"error": "..."} JSON every other /api/ response
// uses.
func routeWebUI(mux *http.ServeMux) http.Handler {
	api := jsonAPIFallback(mux)
	ui := webui.Handler()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			api.ServeHTTP(w, r)
		case r.URL.Path == "/healthz", strings.HasPrefix(r.URL.Path, "/webhooks/"):
			mux.ServeHTTP(w, r)
		default:
			ui.ServeHTTP(w, r)
		}
	})
}
