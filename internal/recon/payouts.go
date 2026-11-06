// payouts.go implements matching a Stripe payout to a bank line: it links
// every webhook payment the payout transferred to the bank line's payout,
// and books the provider's fees as a "Stripe fees" expense.
package recon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/payments"
)

// stripeFeesCategoryName is the expense category MatchPayout books provider
// fees against. It's one of the default categories core.CreateOrg seeds
// (see 0009_expenses.sql); an org that renamed or deleted it gets a 422
// naming it, rather than silently creating a new category.
const stripeFeesCategoryName = "Stripe fees"

// payoutArrivalWindowDays is how many days a payout's arrival date may
// differ from the bank line's posted_on and still be considered the same
// event - payouts sometimes land a day or two either side of what the
// provider predicted.
const payoutArrivalWindowDays = 5

// dollars renders c as "$d.dd" (or "-$d.dd") for client-facing messages.
func dollars(c core.Cents) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, int64(c)/100, int64(c)%100)
}

// PayoutResult summarises a successful MatchPayout.
type PayoutResult struct {
	PaymentsLinked int
	Fees           core.Cents
	FeeExpenseID   *int64
}

// stripeFeeGST is the GST code the fee expense line is given explicitly
// (rather than relying on the "Stripe fees" category's own default), so a
// category whose default was later changed can't silently misstate GST on
// money that genuinely includes it (Stripe AU fees include GST).
var stripeFeeGST = core.GST

// MatchPayout reconciles credit line lineID against orgSlug's payout
// payoutRef: every payment the payout transferred is linked to it, and any
// provider fees are booked as a "Stripe fees" expense dated the line's
// posted_on.
//
// All calls to the payment provider happen before any transaction opens;
// nothing inside the transaction touches the network, so a provider outage
// can never leave a half-written payout. It returns core.ErrInvalidInput
// (422) if payoutRef is empty, if orgSlug has no provider configured, if
// the provider has no such payout, or if fees are owed but the org has no
// "Stripe fees" category; core.ErrInvalidState (409, with a
// core.PublicError message where noted) if the line isn't an unmatched
// credit, if the payout isn't "paid", if its amount or arrival date doesn't
// match the line, if it was already matched to a bank line (public), if any
// payout transaction doesn't map to an existing, not-already-linked payment
// (public, listing the offending transaction refs), or if its transactions'
// net total doesn't equal the payout amount (public) - in every 409 case,
// nothing is written.
func MatchPayout(ctx context.Context, pool *pgxpool.Pool, b *billing.Service, orgID int64, orgSlug string, lineID int64, payoutRef string) (PayoutResult, error) {
	if payoutRef == "" {
		return PayoutResult{}, fmt.Errorf("%w: payout_ref is required", core.ErrInvalidInput)
	}

	provider, err := b.Payments(orgSlug)
	if err != nil {
		if errors.Is(err, payments.ErrNoProvider) {
			return PayoutResult{}, fmt.Errorf("%w: no payment provider configured", core.ErrInvalidInput)
		}
		return PayoutResult{}, err
	}

	payout, err := provider.GetPayout(ctx, payoutRef)
	if err != nil {
		if errors.Is(err, payments.ErrPayoutNotFound) {
			return PayoutResult{}, fmt.Errorf("%w: payout %q not found", core.ErrInvalidInput, payoutRef)
		}
		return PayoutResult{}, fmt.Errorf("fetching payout: %w", err)
	}
	if payout.Status != "paid" {
		return PayoutResult{}, &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout %q is not paid (status %q)", payoutRef, payout.Status)}
	}
	txns, err := provider.PayoutTransactions(ctx, payoutRef)
	if err != nil {
		return PayoutResult{}, fmt.Errorf("fetching payout transactions: %w", err)
	}

	var result PayoutResult
	err = billing.RunInTx(ctx, pool, func(tx pgx.Tx) error {
		line, err := lockBankLine(ctx, tx, orgID, lineID)
		if err != nil {
			return err
		}
		if line.Status != "unmatched" {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("bank line is already %s", line.Status)}
		}
		if line.Amount <= 0 {
			return fmt.Errorf("%w: bank line is not a credit", core.ErrInvalidInput)
		}
		if payout.Amount != line.Amount {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout amount %s does not match the bank line amount %s", dollars(payout.Amount), dollars(line.Amount))}
		}
		if daysBetween(payout.ArrivalOn, line.PostedOn) > payoutArrivalWindowDays {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout arrived %s, more than %d days from the bank line's date %s",
				payout.ArrivalOn.Format("2006-01-02"), payoutArrivalWindowDays, line.PostedOn.Format("2006-01-02"))}
		}

		// Guards both a genuine retry (the same payout ref matched to a
		// second line) and the concurrent case (two requests racing to
		// match the same ref to different lines): this SELECT is the fast
		// path, and the INSERT's unique_violation below is the backstop a
		// concurrent request could still race into.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payouts WHERE org_id = $1 AND provider_ref = $2)`, orgID, payoutRef).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout %q is already matched", payoutRef)}
		}

		var fees, netTotal core.Cents
		var paymentIDs []int64
		var badRefs []string
		for _, txn := range txns {
			switch txn.Type {
			case "payout":
				// The payout's own balance transaction (the net transfer
				// itself) isn't one of the transactions it paid out;
				// skipping it keeps it out of both the ref/sum checks
				// below.
				continue
			case "charge", "payment":
				var paymentID int64
				var existingPayoutID *int64
				err := tx.QueryRow(ctx, `
					SELECT id, payout_id FROM payments
					WHERE org_id = $1 AND provider_ref IN ($2, $3)`,
					orgID, txn.PaymentRef, txn.InvoiceRef,
				).Scan(&paymentID, &existingPayoutID)
				if errors.Is(err, pgx.ErrNoRows) || (err == nil && existingPayoutID != nil) {
					badRefs = append(badRefs, txn.Ref)
					netTotal += txn.Amount - txn.Fee
					continue
				}
				if err != nil {
					return err
				}
				paymentIDs = append(paymentIDs, paymentID)
				fees += txn.Fee
			case "stripe_fee":
				amt := txn.Amount
				if amt < 0 {
					amt = -amt
				}
				fees += amt
			default:
				badRefs = append(badRefs, txn.Ref)
			}
			netTotal += txn.Amount - txn.Fee
		}
		if len(badRefs) > 0 {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout has unmatched or unexpected transactions: %s", strings.Join(badRefs, ", "))}
		}
		if netTotal != payout.Amount {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout transactions net to %d, expected %d", int64(netTotal), int64(payout.Amount))}
		}

		var feeExpenseID *int64
		if fees > 0 {
			var catID int64
			err := tx.QueryRow(ctx, `SELECT id FROM categories WHERE org_id = $1 AND name = $2 AND type = 'expense'`, orgID, stripeFeesCategoryName).Scan(&catID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %q category not found", core.ErrInvalidInput, stripeFeesCategoryName)
			}
			if err != nil {
				return err
			}
			exp, _, err := CreateExpense(ctx, tx, orgID, NewExpense{
				SpentOn:     line.PostedOn,
				Description: fmt.Sprintf("Stripe fees %s", payoutRef),
				Lines:       []NewExpenseLine{{CategoryID: catID, Amount: fees, GSTCode: &stripeFeeGST}},
			})
			if err != nil {
				return err
			}
			feeExpenseID = &exp.ID
		}

		var payoutID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO payouts (org_id, provider_ref, amount, arrival_on, bank_line_id, fee_expense_id)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			orgID, payoutRef, int64(payout.Amount), payout.ArrivalOn, lineID, feeExpenseID,
		).Scan(&payoutID)
		if isUniqueViolation(err) {
			return &core.PublicError{Err: core.ErrInvalidState, Msg: fmt.Sprintf("payout %q is already matched", payoutRef)}
		}
		if err != nil {
			return err
		}

		for _, pid := range paymentIDs {
			if _, err := tx.Exec(ctx, `UPDATE payments SET payout_id = $1, bank_line_id = $2 WHERE org_id = $3 AND id = $4`, payoutID, lineID, orgID, pid); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE bank_lines SET status = 'matched' WHERE org_id = $1 AND id = $2`, orgID, lineID); err != nil {
			return err
		}

		if err := core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "match_payout", map[string]any{
			"payout_ref":     payoutRef,
			"payment_ids":    paymentIDs,
			"fees":           int64(fees),
			"fee_expense_id": feeExpenseID,
		}); err != nil {
			return err
		}

		result = PayoutResult{PaymentsLinked: len(paymentIDs), Fees: fees, FeeExpenseID: feeExpenseID}
		return nil
	})
	if err != nil {
		return PayoutResult{}, err
	}
	return result, nil
}

// unmatchedPayout, if non-nil, is the payouts row for lineID that Unmatch
// found before deciding which reversal path to take.
type unmatchedPayout struct {
	ID           int64
	ProviderRef  string
	FeeExpenseID *int64
}

// findPayoutForLine looks up the payouts row (if any) for orgID's bank line
// lineID, for Unmatch to decide which reversal path applies.
func findPayoutForLine(ctx context.Context, tx core.Querier, orgID, lineID int64) (*unmatchedPayout, error) {
	var p unmatchedPayout
	err := tx.QueryRow(ctx, `SELECT id, provider_ref, fee_expense_id FROM payouts WHERE org_id = $1 AND bank_line_id = $2`, orgID, lineID).
		Scan(&p.ID, &p.ProviderRef, &p.FeeExpenseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// unmatchPayout reverses a payout match: it clears payout_id/bank_line_id
// on every payment the payout linked (never deleting them - they came from
// webhooks, not this module), deletes the fee expense MatchPayout created
// (if any) and the payout row itself, and marks the line unmatched. The
// audit diff names the payout ref, the payment ids unlinked and the fee
// expense id, so this hard delete of the payout/expense rows leaves a
// traceable record like Unmatch's other path.
func unmatchPayout(ctx context.Context, tx core.Querier, orgID, lineID int64, p *unmatchedPayout) error {
	rows, err := tx.Query(ctx, `UPDATE payments SET payout_id = NULL, bank_line_id = NULL WHERE org_id = $1 AND payout_id = $2 RETURNING id`, orgID, p.ID)
	if err != nil {
		return err
	}
	var paymentIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		paymentIDs = append(paymentIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// The payout row is deleted (or at least detached from the expense)
	// before the expense itself: payouts.fee_expense_id references
	// expenses, so deleting the expense first would violate that FK while
	// the payout row still points at it.
	if _, err := tx.Exec(ctx, `DELETE FROM payouts WHERE org_id = $1 AND id = $2`, orgID, p.ID); err != nil {
		return err
	}
	if p.FeeExpenseID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM expenses WHERE org_id = $1 AND id = $2`, orgID, *p.FeeExpenseID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE bank_lines SET status = 'unmatched', ignore_reason = NULL WHERE org_id = $1 AND id = $2`, orgID, lineID); err != nil {
		return err
	}

	if paymentIDs == nil {
		paymentIDs = []int64{}
	}
	return core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "bank_line", lineID, "unmatch", map[string]any{
		"payout_ref":           p.ProviderRef,
		"unlinked_payment_ids": paymentIDs,
		"fee_expense_id":       p.FeeExpenseID,
	})
}
