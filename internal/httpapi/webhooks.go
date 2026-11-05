package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
	"shigoto/internal/payments"
)

// registerWebhookRoutes mounts the provider webhook endpoints. These carry
// no session: authenticity comes from the provider's signature instead.
func registerWebhookRoutes(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("POST /webhooks/stripe/{org}", func(w http.ResponseWriter, r *http.Request) {
		handleStripeWebhook(w, r, d)
	})
}

// handleStripeWebhook verifies and records a Stripe webhook delivery.
// Unknown org or org with no provider configured: 404. Bad signature: 400.
// Otherwise, in one transaction: insert the event id (ON CONFLICT DO
// NOTHING, so a redelivered event is a no-op), then for invoice.paid record
// the payment. An invoice.paid event whose invoice can't be found, or
// whose payment can never be recorded (non-positive amount, invoice not
// payable), is not an error: it's audited (orphan_event or
// payment_rejected) and still returns 200.
func handleStripeWebhook(w http.ResponseWriter, r *http.Request, d Deps) {
	ctx := r.Context()
	slug := r.PathValue("org")

	org, err := core.OrgBySlug(ctx, d.Pool, slug)
	if errors.Is(err, core.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.Error("resolving webhook org failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	provider, err := d.Payments(slug)
	if errors.Is(err, payments.ErrNoProvider) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.Error("resolving webhook provider failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Read the raw body (capped) before any JSON decoding: ParseWebhook
	// verifies the signature over these exact bytes, so it must never go
	// through readJSON's decoder.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	event, err := provider.ParseWebhook(body, r.Header.Get("Stripe-Signature"))
	if errors.Is(err, payments.ErrBadSignature) {
		writeError(w, http.StatusBadRequest, "bad signature")
		return
	}
	if err != nil {
		slog.Error("parsing webhook failed", "error", err)
		writeError(w, http.StatusBadRequest, "invalid webhook payload")
		return
	}

	err = runInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO webhook_events (provider, event_id, org_id) VALUES ('stripe', $1, $2) ON CONFLICT DO NOTHING`,
			event.ID, org.ID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Already recorded: redelivery, do nothing more.
			return nil
		}

		if event.Kind != "invoice.paid" {
			return nil
		}

		// Match on provider_ref, falling back to the shigoto id the
		// provider echoed from metadata (scoped to this org): a send whose
		// provider_ref was never committed can still be paid.
		var invoiceID int64
		matchedBy := "provider_ref"
		err = tx.QueryRow(ctx,
			`SELECT id FROM invoices WHERE org_id = $1 AND provider_ref = $2`,
			org.ID, event.InvoiceRef,
		).Scan(&invoiceID)
		if errors.Is(err, pgx.ErrNoRows) && event.ShigotoInvoiceID != 0 {
			matchedBy = "metadata"
			err = tx.QueryRow(ctx,
				`SELECT id FROM invoices WHERE org_id = $1 AND id = $2`,
				org.ID, event.ShigotoInvoiceID,
			).Scan(&invoiceID)
			if err == nil {
				// Backfill the provider_ref shigoto never committed (e.g.
				// a send whose response was lost after Stripe accepted
				// it), so future webhooks for this invoice match on
				// provider_ref directly. Guarded by provider_ref IS NULL:
				// never overwrite one already recorded.
				if _, err := tx.Exec(ctx,
					`UPDATE invoices SET provider_ref=$1 WHERE id=$2 AND org_id=$3 AND provider_ref IS NULL`,
					event.InvoiceRef, invoiceID, org.ID,
				); err != nil {
					return err
				}
			}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return core.Audit(ctx, tx, org.ID, nil, "invoice", 0, "orphan_event", map[string]any{
				"event_id":           event.ID,
				"invoice_ref":        event.InvoiceRef,
				"shigoto_invoice_id": event.ShigotoInvoiceID,
			})
		}
		if err != nil {
			return err
		}

		// Rejections that a redelivery can never fix are audited and
		// acknowledged (200), not returned as a 500 that Stripe would
		// retry for days.
		reject := func(reason string) error {
			return core.Audit(ctx, tx, org.ID, nil, "invoice", invoiceID, "payment_rejected", map[string]any{
				"event_id": event.ID,
				"amount":   event.Amount,
				"reason":   reason,
			})
		}
		if event.Amount <= 0 {
			return reject("non-positive amount")
		}

		payment, created, err := core.RecordPayment(ctx, tx, org.ID, core.NewPayment{
			InvoiceID:   invoiceID,
			Amount:      event.Amount,
			PaidOn:      event.PaidOn,
			Method:      "stripe",
			ProviderRef: event.PaymentRef,
		})
		if errors.Is(err, core.ErrInvalidState) {
			return reject("invoice not payable or conflicting payment ref")
		}
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		return core.Audit(ctx, tx, org.ID, nil, "payment", payment.ID, "recorded_by_webhook", map[string]any{
			"event_id":   event.ID,
			"invoice_id": invoiceID,
			"matched_by": matchedBy,
		})
	})
	if err != nil {
		slog.Error("recording webhook event failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusOK)
}
