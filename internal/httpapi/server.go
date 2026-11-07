package httpapi

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/mail"
	"shigoto/internal/module"
	"shigoto/internal/payments"
	"shigoto/internal/payments/stripe"
	"shigoto/internal/portal"
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
	mux.HandleFunc("POST /api/v1/auth/magic", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		requestMagicLink(w, r, d)
	})
	mux.HandleFunc("POST /api/v1/auth/magic/verify", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		verifyMagicLink(w, r, d)
	})

	registerCoreRoutes(mux, d)
	registerSettingsRoutes(mux, d)
	registerWebhookRoutes(mux, d)
	registerMeRoutes(mux, d)
	registerDashboardRoutes(mux, d)
	registerMemberRoutes(mux, d)
	mux.Handle("POST /api/v1/orgs/{org}/contacts/{id}/portal-invite",
		d.Auth.RequireOrg(auth.PermContactsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createPortalInvite(w, r, d)
		})))

	md := module.Deps{Pool: d.Pool, Auth: d.Auth, Cfg: d.Cfg, Billing: d.Billing}
	portal.RegisterRoutes(mux, md)
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

// --- magic-link sign-in ---

// magicMailTimeout bounds the background goroutine requestMagicLink spawns
// to actually send the email (Ruling 61): the SMTP round trip must never
// sit on the request path.
const magicMailTimeout = 15 * time.Second

// sendMagicLinkEmail sends link to email, if d.Mailer is configured, and
// reports whether it actually went out. requestMagicLink never surfaces
// this result to the caller (the response is always 202 {}, per
// global-constraints.md's no-enumeration rule); createPortalInvite does
// surface it, the same way sendInviteEmail's result feeds createInvite's
// response.
func sendMagicLinkEmail(ctx context.Context, d Deps, email, link string) bool {
	if d.Mailer == nil {
		return false
	}
	safeLink := html.EscapeString(link)
	msg := mail.Message{
		To:      email,
		Subject: "Sign in to shigoto",
		Text:    fmt.Sprintf("Use this link to sign in to shigoto. It expires in 15 minutes: %s", link),
		HTML:    fmt.Sprintf(`<p>Use this link to sign in to shigoto. It expires in 15 minutes.</p><p><a href="%s">Sign in</a></p>`, safeLink),
	}
	if err := d.Mailer.Send(ctx, msg); err != nil {
		slog.Error("sending magic link email failed", "error", err)
		return false
	}
	return true
}

// requestMagicLink handles POST /api/v1/auth/magic with body {email}.
// Ruling 58: with no mailer configured, this always 422s ("email is not
// configured") regardless of the email given - a config-level failure, not
// something that could leak whether an account exists. Otherwise it always
// responds 202 {}, whether or not the email is known, belongs to a staff-
// only user, or is over the rate limit (which gets 429 instead, per
// global-constraints.md "Rate limits" - that status itself doesn't leak
// anything, since it's keyed by email regardless of whether the email
// exists): global-constraints.md's no-enumeration rule extends to every one
// of the 202 cases being indistinguishable from the outside.
func requestMagicLink(w http.ResponseWriter, r *http.Request, d Deps) {
	if d.Mailer == nil {
		writeError(w, http.StatusUnprocessableEntity, "email is not configured")
		return
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	email := strings.TrimSpace(body.Email)

	// Checked (and reserved) before the lookup: every request counts
	// towards the limit, eligible or not (global-constraints.md "Rate
	// limits") - a request over the limit gets 429, not the usual 202.
	if ok, retryAfter := d.Auth.AllowMagicRequest(email); !ok {
		auth.WriteTooManyRequests(w, retryAfter)
		return
	}

	token, err := auth.RequestMagicLink(r.Context(), d.Pool, email)
	if err != nil {
		slog.Error("requesting magic link failed", "error", err)
		writeJSON(w, http.StatusAccepted, map[string]any{})
		return
	}
	if token != "" {
		link := strings.TrimRight(d.Cfg.BaseURL, "/") + "/auth/magic?token=" + token
		// Ruling 61: the SMTP round trip must never sit on the request
		// path. context.WithoutCancel detaches from the request's own
		// context (which is cancelled the moment this handler returns),
		// bounded instead by its own timeout; a send failure is logged,
		// never surfaced (the response is already written).
		go func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("magic-link mail panicked", "panic", p)
				}
			}()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), magicMailTimeout)
			defer cancel()
			sendMagicLinkEmail(ctx, d, email, link)
		}()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{})
}

// verifyMagicLink handles POST /api/v1/auth/magic/verify with body {token}:
// the SPA's own /auth/magic?token=... page posts here. On success it sets
// the session cookie (via auth.Auth.VerifyMagicLink) and tells the SPA
// where to go next.
func verifyMagicLink(w http.ResponseWriter, r *http.Request, d Deps) {
	var body struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := d.Auth.VerifyMagicLink(r.Context(), w, body.Token)
	if errors.Is(err, auth.ErrMagicLinkInvalid) {
		writeError(w, http.StatusGone, err.Error())
		return
	}
	if err != nil {
		slog.Error("verifying magic link failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/portal"})
}

// --- portal invite ---

// errPortalContactConflict is the public 409 for inviting a contact to the
// portal when the matching user (found or created by email) already holds a
// membership in this org for a *different* contact, or a staff role (which
// always has a nil contact_id, so it can never match contact.ID either).
var errPortalContactConflict = &core.PublicError{
	Err: core.ErrInvalidState,
	Msg: "this email already belongs to a different member of this organisation",
}

// createPortalInvite handles POST
// /api/v1/orgs/{org}/contacts/{id}/portal-invite: it finds or creates a
// (passwordless) user account for the contact's email, gives it a
// tenant/client membership tied to this contact, and emails a magic sign-in
// link. Re-inviting the same already-portal-linked contact is idempotent
// (200, link resent); a conflicting existing membership (a different
// contact, or any staff role) is a 409.
func createPortalInvite(w http.ResponseWriter, r *http.Request, d Deps) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	contact, err := core.GetContact(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if contact.Kind != "tenant" && contact.Kind != "client" {
		writeError(w, http.StatusUnprocessableEntity, "contact must be a tenant or client")
		return
	}
	if contact.Email == "" {
		writeError(w, http.StatusUnprocessableEntity, "contact has no email")
		return
	}

	var userID int64
	err = runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var newUser bool
		// ON CONFLICT (email) DO NOTHING + re-select: two concurrent
		// portal-invites for the same not-yet-existing email would
		// otherwise both race the plain SELECT-then-INSERT and one would
		// hit the unique violation on users.email as a raw 500.
		err := tx.QueryRow(r.Context(),
			`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, NULL)
			 ON CONFLICT (email) DO NOTHING
			 RETURNING id`,
			contact.Email, contact.Name,
		).Scan(&userID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if err := tx.QueryRow(r.Context(), "SELECT id FROM users WHERE email = $1", contact.Email).Scan(&userID); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			newUser = true
		}

		// Same idea for the membership row: ON CONFLICT DO NOTHING (its
		// primary key is (user_id, org_id)) rather than SELECT-then-INSERT,
		// so a race with another concurrent invite - or simply re-inviting
		// an existing member - never 500s. The re-select below is what
		// actually decides idempotent-resend vs. conflict, regardless of
		// whether this INSERT did anything.
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO memberships (user_id, org_id, role, contact_id) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (user_id, org_id) DO NOTHING`,
			userID, p.OrgID, contact.Kind, contact.ID,
		); err != nil {
			return err
		}

		var existingContactID *int64
		if err := tx.QueryRow(r.Context(),
			"SELECT contact_id FROM memberships WHERE org_id = $1 AND user_id = $2",
			p.OrgID, userID,
		).Scan(&existingContactID); err != nil {
			return err
		}
		if existingContactID == nil || *existingContactID != contact.ID {
			// A different contact, or a staff role (always nil
			// contact_id): never silently relinked to this contact.
			return errPortalContactConflict
		}
		// Else: same contact already a member (whether the INSERT above
		// just created that row or it already existed) - idempotent, fall
		// through to resend the link.

		if newUser {
			if err := core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "user", userID, "create", map[string]string{"email": contact.Email}); err != nil {
				return err
			}
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "membership", userID, "portal_invite", map[string]any{"contact_id": contact.ID})
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	// Ruling 60: a user who already belongs to some other org must never
	// have their magic link handed straight back to this org's caller -
	// that would let a staff member in org A read off a working sign-in
	// link for org B's tenant just by "inviting" their known email address
	// here. The email itself is still fine to send (it goes to the
	// contact's real inbox, not to this response), but the link is never
	// included in the JSON when that's the only way it could be read off,
	// and when nothing was actually sent, there's nothing else to tell the
	// caller except to fix email delivery or point the person at /login.
	var otherOrgMember bool
	if err := d.Pool.QueryRow(r.Context(),
		"SELECT EXISTS(SELECT 1 FROM memberships WHERE user_id = $1 AND org_id != $2)",
		userID, p.OrgID,
	).Scan(&otherOrgMember); err != nil {
		slog.Error("checking portal invite cross-org membership failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	token, err := auth.IssueMagicLinkForUser(r.Context(), d.Pool, userID)
	if err != nil {
		slog.Error("issuing portal magic link failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	link := strings.TrimRight(d.Cfg.BaseURL, "/") + "/auth/magic?token=" + token
	emailed := sendMagicLinkEmail(r.Context(), d, contact.Email, link)

	resp := map[string]any{"emailed": emailed}
	if !emailed {
		if otherOrgMember {
			resp["message"] = "This person already has an account in another organisation. Configure email to send them a link, or have them request one at /login."
		} else {
			resp["link"] = link
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
