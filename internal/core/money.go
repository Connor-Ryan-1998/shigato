package core

import "fmt"

// Cents represents an amount of money in cents.
type Cents int64

// GSTCode is a GST tax classification.
type GSTCode string

const (
	// GST is the standard goods and services tax (10% in Australia).
	GST GSTCode = "GST"
	// FRE is GST-free.
	FRE GSTCode = "FRE"
	// INP is input tax-only (no output tax).
	INP GSTCode = "INP"
	// NT is not taxable.
	NT GSTCode = "N-T"
)

// ParseGSTCode parses a string into a GSTCode, or returns an error if invalid.
func ParseGSTCode(s string) (GSTCode, error) {
	switch s {
	case "GST", "FRE", "INP", "N-T":
		return GSTCode(s), nil
	default:
		return "", fmt.Errorf("invalid GST code: %q", s)
	}
}

// DivRound rounds n/d to the nearest integer, rounding half away from zero.
// Uses integer arithmetic: (2n+d)/(2d) for n>=0, negates for n<0. Exported
// so other packages (e.g. property's rent-period proration) share the same
// rounding rule as invoice/GST line math.
func DivRound(n, d int64) int64 {
	if n >= 0 {
		return (2*n + d) / (2 * d)
	}
	// For negative, negate, compute, negate.
	return -((2*(-n) + d) / (2 * d))
}

// LineAmount calculates the total amount for a line: quantityMilli × unit / 1000,
// rounded half away from zero. quantityMilli is in thousandths of a unit.
func LineAmount(quantityMilli int64, unit Cents) Cents {
	return Cents(DivRound(quantityMilli*int64(unit), 1000))
}

// GSTOnExclusive calculates GST on an exclusive (pre-tax) amount.
// For code == GST, it returns amount / 10 rounded half away from zero.
// For other codes, it returns 0.
// The comment documents the rounding rule: GST is calculated per invoice or expense line
// and rounded half away from zero to the cent.
func GSTOnExclusive(amount Cents, code GSTCode) Cents {
	if code != GST {
		return 0
	}
	return Cents(DivRound(int64(amount), 10))
}

// GSTInInclusive calculates GST from a total (inclusive) amount.
// For code == GST, it returns total / 11 rounded half away from zero.
// For other codes, it returns 0.
func GSTInInclusive(total Cents, code GSTCode) Cents {
	if code != GST {
		return 0
	}
	return Cents(DivRound(int64(total), 11))
}

// Line represents a single invoice or expense line.
type Line struct {
	QuantityMilli int64
	Unit          Cents
	Code          GSTCode
}

// Totals calculates the subtotal, GST, and total for a list of lines.
// GST is calculated per line and then summed (per-line rounding).
func Totals(lines []Line) (subtotal, gst, total Cents) {
	for _, line := range lines {
		lineAmount := LineAmount(line.QuantityMilli, line.Unit)
		subtotal += lineAmount
		gst += GSTOnExclusive(lineAmount, line.Code)
	}
	total = subtotal + gst
	return
}
