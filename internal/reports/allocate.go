// Package reports produces the cash-basis P&L and Australian BAS, with
// CSV export. It deliberately does not import internal/recon,
// internal/property or internal/timesheets - those are toggleable modules,
// but reports are core (always on) - so IncomeRows and ExpenseRows query
// invoices/payments/expenses/properties/projects directly with SQL instead
// of going through those packages' exported funcs.
package reports

import (
	"math/big"

	"shigoto/internal/core"
)

// mulDivRound keeps intermediate products and rounding exact for valid int64 totals.
func mulDivRound(a, b, d int64) int64 {
	var n, denominator big.Int
	n.Mul(big.NewInt(a), big.NewInt(b))
	sign := n.Sign()
	n.Abs(&n)
	n.Lsh(&n, 1)
	n.Add(&n, big.NewInt(d))
	denominator.Lsh(big.NewInt(d), 1)
	n.Quo(&n, &denominator)
	if sign < 0 {
		n.Neg(&n)
	}
	return n.Int64()
}

// AllocLine is one invoice line being allocated a share of a payment.
// Amount is ex-GST; GST is that line's own GST amount.
type AllocLine struct {
	Amount, GST core.Cents
	Code        core.GSTCode
	PropertyID  *int64
	ProjectID   *int64
}

// Share is one AllocLine's share of an allocated payment.
type Share struct {
	Line          AllocLine
	Incl, GST, Ex core.Cents
}

// Allocate splits payment across lines proportionally to each line's
// incl-GST total, per the Global Constraints allocation rule: each line's
// incl-GST share is DivRound(payment × line_total, invoice_total), except
// the last line, which takes whatever remainder makes the shares sum to
// payment exactly. Each share's GST part is DivRound(share × line.GST,
// line_total); its ex-GST part is the share minus that GST part.
func Allocate(payment core.Cents, lines []AllocLine) []Share {
	if len(lines) == 0 {
		return nil
	}

	lineTotals := make([]core.Cents, len(lines))
	var invoiceTotal core.Cents
	for i, l := range lines {
		lineTotals[i] = l.Amount + l.GST
		invoiceTotal += lineTotals[i]
	}

	shares := make([]Share, len(lines))
	var allocated core.Cents
	for i, l := range lines {
		var incl core.Cents
		switch {
		case i == len(lines)-1:
			incl = payment - allocated
		case invoiceTotal != 0:
			incl = core.Cents(mulDivRound(int64(payment), int64(lineTotals[i]), int64(invoiceTotal)))
		}
		allocated += incl

		var gst core.Cents
		if lineTotals[i] != 0 {
			gst = core.Cents(mulDivRound(int64(incl), int64(l.GST), int64(lineTotals[i])))
		}

		shares[i] = Share{Line: l, Incl: incl, GST: gst, Ex: incl - gst}
	}
	return shares
}
