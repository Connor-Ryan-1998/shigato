package property

import (
	"sort"
	"time"

	"shigoto/internal/core"
)

// DailyRate returns the exact rational daily rate for amount at frequency f,
// as num/den: weekly amount/7, fortnightly amount/14, monthly amount*12/365.
// Callers multiply by a day count and round with core.DivRound; den is never
// divided out early so each caller controls its own rounding point.
func DailyRate(f Frequency, amount core.Cents) (num, den int64) {
	switch f {
	case Weekly:
		return int64(amount), 7
	case Fortnightly:
		return int64(amount), 14
	case Monthly:
		return int64(amount) * 12, 365
	default:
		return 0, 1
	}
}

// periodStart returns the Start date of period n (0-indexed) anchored on
// t.Start. Every call computes directly from t.Start, never from a previous
// period, so there is no drift.
func periodStart(t LeaseTerms, n int) time.Time {
	switch t.Frequency {
	case Weekly:
		return t.Start.AddDate(0, 0, n*7)
	case Fortnightly:
		return t.Start.AddDate(0, 0, n*14)
	case Monthly:
		y, m, d := t.Start.Date()
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
		lastDay := first.AddDate(0, 1, -1).Day()
		if d > lastDay {
			d = lastDay
		}
		return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
	default:
		return t.Start
	}
}

// daysInclusive returns the number of whole days from a to b inclusive of
// both ends. Both must be UTC midnight for this to be exact (no DST).
func daysInclusive(a, b time.Time) int64 {
	return int64(b.Sub(a).Hours()/24) + 1
}

// rateAt returns the rent amount effective on date, from changes sorted
// ascending by Effective. It is the amount of the latest change whose
// Effective is on or before date.
func rateAt(changes []RentChange, date time.Time) core.Cents {
	var amount core.Cents
	for _, c := range changes {
		if c.Effective.After(date) {
			break
		}
		amount = c.Amount
	}
	return amount
}

// proratedAmount returns days of amount at frequency f, as a daily-rate
// proration rounded half away from zero.
func proratedAmount(f Frequency, amount core.Cents, days int64) core.Cents {
	num, den := DailyRate(f, amount)
	return core.Cents(core.DivRound(days*num, den))
}

// buildPeriod computes the Period for [pStart, pEnd], where natEnd is the
// period's natural (untruncated) end: pEnd < natEnd means the period was cut
// short by the lease's End.
func buildPeriod(t LeaseTerms, pStart, pEnd, natEnd time.Time) Period {
	truncated := pEnd.Before(natEnd)

	// Breakpoints: rent-change effective dates strictly inside the period
	// (after its start, on or before its end). A change effective exactly
	// at the next period's start never lands here.
	var breaks []time.Time
	for _, c := range t.Changes {
		if c.Effective.After(pStart) && !c.Effective.After(pEnd) {
			breaks = append(breaks, c.Effective)
		}
	}
	sort.Slice(breaks, func(i, j int) bool { return breaks[i].Before(breaks[j]) })

	var rent core.Cents
	switch {
	case len(breaks) == 0 && !truncated:
		// A full period at one rate is charged the exact period amount.
		rent = rateAt(t.Changes, pStart)
	case len(breaks) == 0:
		// Truncated final period: prorate the whole span over its days.
		rent = proratedAmount(t.Frequency, rateAt(t.Changes, pStart), daysInclusive(pStart, pEnd))
	default:
		// Rent-change split: each segment is days x daily rate, rounded
		// individually, then summed.
		segStart := pStart
		for _, bp := range breaks {
			segEnd := bp.AddDate(0, 0, -1)
			rent += proratedAmount(t.Frequency, rateAt(t.Changes, segStart), daysInclusive(segStart, segEnd))
			segStart = bp
		}
		rent += proratedAmount(t.Frequency, rateAt(t.Changes, segStart), daysInclusive(segStart, pEnd))
	}

	return Period{Start: pStart, End: pEnd, Rent: rent, Charges: chargeLines(t, pStart, pEnd, truncated)}
}

// chargeLines returns the ChargeLines for charges active on pStart (Start <=
// pStart and End nil or End >= pStart). Charges never split at rent-change
// boundaries; they're prorated only by the period's own truncation, exactly
// like rent.
func chargeLines(t LeaseTerms, pStart, pEnd time.Time, truncated bool) []ChargeLine {
	var lines []ChargeLine
	for _, c := range t.Charges {
		if c.Start.After(pStart) {
			continue
		}
		if c.End != nil && c.End.Before(pStart) {
			continue
		}
		amount := c.Amount
		if truncated {
			amount = proratedAmount(t.Frequency, c.Amount, daysInclusive(pStart, pEnd))
		}
		lines = append(lines, ChargeLine{Description: c.Description, Amount: amount})
	}
	return lines
}

// PeriodsToInvoice returns every rent period of t whose Start is after
// PaidTo (or every period from Start if PaidTo is nil) and whose Start is on
// or before asOf+leadDays, stopping once a period's Start would fall after
// t.End. It is pure and deterministic: no DB access, no wall clock.
func PeriodsToInvoice(t LeaseTerms, asOf time.Time, leadDays int) []Period {
	cutoff := asOf.AddDate(0, 0, leadDays)

	var periods []Period
	for n := 0; ; n++ {
		pStart := periodStart(t, n)
		if t.End != nil && pStart.After(*t.End) {
			break
		}
		if pStart.After(cutoff) {
			break
		}

		natEnd := periodStart(t, n+1).AddDate(0, 0, -1)
		pEnd := natEnd
		if t.End != nil && pEnd.After(*t.End) {
			pEnd = *t.End
		}

		if t.PaidTo != nil && !pStart.After(*t.PaidTo) {
			continue
		}

		periods = append(periods, buildPeriod(t, pStart, pEnd, natEnd))
	}
	return periods
}
