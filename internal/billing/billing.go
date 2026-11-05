// Package billing owns the money-moving half of an invoice's lifecycle:
// issuing (QLD pay-options rule, numbering, best-effort send), retried
// sending and voiding. It wraps the transactions, context.WithoutCancel
// handling and audit entries each of those needs so httpapi's handlers stay
// thin HTTP wrappers.
package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/payments"
)

// ErrTooFewPayOptions is the QLD rule: a rent or bond invoice needs at
// least minPayOptions ways to pay before it can be issued.
var ErrTooFewPayOptions = errors.New("rent and bond invoices need at least 2 payment options")

// ErrProviderVoidFailed wraps a payments.Provider error from Void, so
// callers can tell a provider-side void failure (502) apart from a local
// state error (409/404).
var ErrProviderVoidFailed = errors.New("void failed with payment provider")

// ErrDueDatePassed is returned when sending an invoice to the payment
// provider after its due date: the provider would present it as already
// overdue, so it must be voided and reissued instead.
var ErrDueDatePassed = errors.New("due date has passed; void and reissue")

// minPayOptions is the QLD rule: a rent or bond invoice needs at least this
// many ways to pay before it can be issued.
const minPayOptions = 2

// detachTimeout bounds how long a detached (context.WithoutCancel) provider
// call may run after the triggering request's context is gone, so a
// provider outage can't hang Issue/Send/Void's goroutine forever.
const detachTimeout = 2 * time.Minute

// detach returns a context that survives ctx's cancellation (e.g. a client
// disconnect) but is bounded by detachTimeout, for provider work that must
// finish once started.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachTimeout)
}

// actorKey is the context key WithActor/ActorFrom use to pass the acting
// user through to an audit_log entry.
type actorKey struct{}

// WithActor returns a context carrying userID as the actor for any audit
// entries Issue, Send or Void write. Callers with no user in scope (e.g.
// the scheduler) can omit it; audit_log.actor_user_id is then NULL.
func WithActor(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

// ActorFrom reads the actor set by WithActor, or nil if none was set.
// Exported so other packages whose transactional work must audit under the
// same actor (e.g. timesheets' billing runs) don't need their own copy of
// the context key.
func ActorFrom(ctx context.Context) *int64 {
	if id, ok := ctx.Value(actorKey{}).(int64); ok {
		return &id
	}
	return nil
}

// Service implements invoice issuing, sending and voiding against Postgres
// and an org's payment provider.
type Service struct {
	Pool *pgxpool.Pool
	// GetEnv reads environment variables for config.StripeFor. It
	// defaults to os.Getenv in httpapi.New; tests inject a fake.
	GetEnv func(string) string
	// Payments resolves the payments.Provider for an org slug, returning
	// payments.ErrNoProvider when the org has none configured.
	Payments func(orgSlug string) (payments.Provider, error)
	// Location is used for Issue's issue date and any other "today" the
	// service needs.
	Location *time.Location
}

// New builds a Service against pool: resolve reads an org's payment
// provider (e.g. httpapi's defaultPayments) and getenv reads environment
// variables for config.StripeFor. loc is used for Issue's issue date and
// any other "today" the service needs.
func New(pool *pgxpool.Pool, getenv func(string) string, resolve func(orgSlug string) (payments.Provider, error), loc *time.Location) *Service {
	return &Service{Pool: pool, GetEnv: getenv, Payments: resolve, Location: loc}
}

// loc returns s.Location, defaulting to UTC so a Service built without one
// set (e.g. a non-HTTP caller) doesn't panic calling time.Now().In(nil).
func (s *Service) loc() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

// RunInTx runs fn in a new transaction on pool, committing on success and
// rolling back (a no-op once committed) otherwise. Exported so other
// packages whose transactional work must share this exact commit/rollback
// shape (e.g. timesheets' billing runs) don't need their own copy.
func RunInTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PayOptions builds the pay_options an org currently offers: bank transfer
// when it has both a BSB and account number, PayID when it has one, and
// Stripe when config.StripeFor finds both a secret key and a webhook
// secret for its slug (a secret key alone can create charges but can't
// verify the webhooks that confirm they were paid).
func (s *Service) PayOptions(ctx context.Context, q core.Querier, orgID int64) ([]string, error) {
	var slug string
	var bsb, account, payid sql.NullString
	err := q.QueryRow(ctx, `SELECT slug, bank_bsb, bank_account, payid FROM orgs WHERE id = $1`, orgID).
		Scan(&slug, &bsb, &account, &payid)
	if err != nil {
		return nil, err
	}

	opts := []string{}
	if bsb.String != "" && account.String != "" {
		opts = append(opts, "bank_transfer")
	}
	if payid.String != "" {
		opts = append(opts, "payid")
	}
	if org, ok := config.StripeFor(s.GetEnv, slug); ok && org.SecretKey != "" && org.WebhookSecret != "" {
		opts = append(opts, "stripe")
	}
	return opts, nil
}

// Issue enforces the QLD pay-options rule (rent and bond invoices need at
// least 2, returning ErrTooFewPayOptions and leaving the invoice a draft
// otherwise), assigns the gap-free invoice number, and audits the issue -
// all in one transaction. Once that's committed, it makes a best-effort
// attempt to send the invoice to the org's payment provider in a detached
// context (see detach): a client disconnect mid-send must not abandon a
// provider call, or the commit recording it, half-done. A failed
// send (other than payments.ErrNoProvider) is logged, not returned: the
// number is already burned and won't move, and the invoice stays open with
// no provider_ref for POST .../send to retry.
func (s *Service) Issue(ctx context.Context, orgID int64, orgSlug string, invoiceID int64) (core.Invoice, error) {
	actor := ActorFrom(ctx)

	var inv core.Invoice
	err := RunInTx(ctx, s.Pool, func(tx pgx.Tx) error {
		existing, err := core.GetInvoice(ctx, tx, orgID, invoiceID)
		if err != nil {
			return err
		}

		if existing.Source == "rent" || existing.Source == "bond" {
			opts, err := s.PayOptions(ctx, tx, orgID)
			if err != nil {
				return err
			}
			if len(opts) < minPayOptions {
				return ErrTooFewPayOptions
			}
		}

		inv, err = core.IssueInvoice(ctx, tx, orgID, invoiceID, time.Now().In(s.loc()))
		if err != nil {
			return err
		}
		return core.Audit(ctx, tx, orgID, actor, "invoice", inv.ID, "issue", map[string]any{"number": inv.Number})
	})
	if err != nil {
		return core.Invoice{}, err
	}

	dctx, cancel := detach(ctx)
	defer cancel()
	if err := s.sendToProvider(dctx, orgID, orgSlug, &inv); err != nil && !errors.Is(err, payments.ErrNoProvider) {
		slog.Error("sending invoice to provider failed", "invoice_id", inv.ID, "error", err)
	}

	return inv, nil
}

// Send retries sending an issued invoice to the org's payment provider
// after a previous attempt failed. Only an 'open' invoice with no
// provider_ref yet can be sent; a previously-sent invoice (provider_ref
// already set) returns core.ErrInvalidState rather than sending a second
// time.
func (s *Service) Send(ctx context.Context, orgID int64, orgSlug string, invoiceID int64) (core.Invoice, error) {
	inv, err := core.GetInvoice(ctx, s.Pool, orgID, invoiceID)
	if err != nil {
		return core.Invoice{}, err
	}
	if inv.Status != "open" || inv.ProviderRef != "" {
		return core.Invoice{}, core.ErrInvalidState
	}

	// Detached from the request, as in Issue.
	dctx, cancel := detach(ctx)
	defer cancel()
	if err := s.sendToProvider(dctx, orgID, orgSlug, &inv); err != nil {
		return core.Invoice{}, err
	}
	return inv, nil
}

// sendToProvider resolves orgSlug's payments.Provider, ensures the
// invoice's contact has a provider customer ref (creating one if needed),
// sends the invoice, and stores the returned provider_ref/provider_url.
// inv is updated in place on success. Returns payments.ErrNoProvider
// unchanged when the org has no provider configured, so callers can treat
// that as "nothing to do" rather than a failure, and ErrDueDatePassed
// (before any provider call) once inv's due date is behind today in
// s.Location.
//
// It re-reads and locks the invoice row (FOR UPDATE) before sending and
// re-checks provider_ref there, returning core.ErrInvalidState if another
// call already sent it: the caller's own pre-check (e.g. Send's) is only a
// fast path, since two concurrent sends (an explicit retry racing Issue's
// automatic attempt, or two retries) could otherwise both pass it and
// double-send to the provider.
func (s *Service) sendToProvider(ctx context.Context, orgID int64, orgSlug string, inv *core.Invoice) error {
	provider, err := s.Payments(orgSlug)
	if err != nil {
		return err
	}

	// Checked before any provider call (EnsureCustomer included); the
	// provider's own due-date check stays as a backstop.
	now := time.Now().In(s.loc())
	y, m, d := now.Date()
	if inv.DueDate != nil && inv.DueDate.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) {
		return ErrDueDatePassed
	}

	return RunInTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var providerRef string
		var contactID int64
		if err := tx.QueryRow(ctx,
			`SELECT contact_id, coalesce(provider_ref,'') FROM invoices WHERE org_id = $1 AND id = $2 FOR UPDATE`,
			orgID, inv.ID,
		).Scan(&contactID, &providerRef); err != nil {
			return err
		}
		if providerRef != "" {
			return core.ErrInvalidState
		}

		contact, err := core.GetContact(ctx, tx, orgID, contactID)
		if err != nil {
			return err
		}

		custRef := contact.ProviderCustomerRef
		if custRef == "" {
			custRef, err = provider.EnsureCustomer(ctx, contact)
			if err != nil {
				return fmt.Errorf("ensuring provider customer: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE contacts SET provider_customer_ref = $1 WHERE org_id = $2 AND id = $3`,
				custRef, orgID, contact.ID,
			); err != nil {
				return err
			}
		}

		ref, hostedURL, err := provider.SendInvoice(ctx, *inv, inv.Lines, custRef, now)
		if err != nil {
			return fmt.Errorf("sending invoice: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE invoices SET provider_ref = $1, provider_url = $2 WHERE org_id = $3 AND id = $4`,
			ref, hostedURL, orgID, inv.ID,
		); err != nil {
			return err
		}

		inv.ProviderRef = ref
		inv.ProviderURL = hostedURL
		return nil
	})
}

// Void voids an invoice and, if it was already sent to a payment provider,
// voids it there too, inside the same transaction: a shigoto void with no
// corresponding provider void would leave the invoice payable on the
// provider's side, so if the provider call fails the whole transaction
// (including the local void) rolls back and the two sides never disagree.
// The returned error wraps ErrProviderVoidFailed when the provider call is
// what failed, so callers can map it to 502 rather than a local-state
// status.
//
// ctx is detached from the caller's own (see detach) before any work
// starts: once the provider void is under way, a client disconnect must
// not roll back the local void and leave the two sides disagreeing.
func (s *Service) Void(ctx context.Context, orgID int64, orgSlug string, invoiceID int64) (core.Invoice, error) {
	actor := ActorFrom(ctx)
	ctx, cancel := detach(ctx)
	defer cancel()

	var inv core.Invoice
	var providerErr error
	err := RunInTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		inv, err = core.VoidInvoice(ctx, tx, orgID, invoiceID)
		if err != nil {
			return err
		}
		// Unlock any time entries this invoice's lines had billed, in the
		// same tx as the void itself (and before the provider call below),
		// so a provider failure rolling the transaction back also leaves
		// the entries locked, matching the invoice staying open.
		if _, err := tx.Exec(ctx,
			`UPDATE time_entries SET invoice_line_id = NULL WHERE org_id = $1 AND invoice_line_id IN (SELECT id FROM invoice_lines WHERE org_id = $1 AND invoice_id = $2)`,
			orgID, inv.ID,
		); err != nil {
			return err
		}
		if err := core.Audit(ctx, tx, orgID, actor, "invoice", inv.ID, "void", nil); err != nil {
			return err
		}

		if inv.ProviderRef == "" {
			return nil
		}
		provider, err := s.Payments(orgSlug)
		if err != nil {
			providerErr = err
			return err
		}
		if err := provider.VoidInvoice(ctx, inv.ProviderRef); err != nil {
			providerErr = err
			return err
		}
		return nil
	})
	if err != nil {
		if providerErr != nil {
			return core.Invoice{}, fmt.Errorf("%w: %w", ErrProviderVoidFailed, providerErr)
		}
		return core.Invoice{}, err
	}
	return inv, nil
}
