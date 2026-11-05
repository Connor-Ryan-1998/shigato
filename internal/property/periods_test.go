package property

import (
	"testing"
	"time"

	"shigoto/internal/core"
)

func d(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func dp(s string) *time.Time {
	t := d(s)
	return &t
}

func assertPeriod(t *testing.T, got Period, wantStart, wantEnd string, wantRent core.Cents) {
	t.Helper()
	if !got.Start.Equal(d(wantStart)) || !got.End.Equal(d(wantEnd)) {
		t.Errorf("period = [%s..%s], want [%s..%s]",
			got.Start.Format("2006-01-02"), got.End.Format("2006-01-02"), wantStart, wantEnd)
	}
	if got.Rent != wantRent {
		t.Errorf("period [%s..%s] rent = %d, want %d", wantStart, wantEnd, got.Rent, wantRent)
	}
}

func TestPeriodsToInvoiceWeeklyTwoPeriods(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Weekly,
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 50000}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-02"), 7)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	assertPeriod(t, got[0], "2026-11-02", "2026-11-08", 50000)
	assertPeriod(t, got[1], "2026-11-09", "2026-11-15", 50000)
}

func TestPeriodsToInvoiceFortnightlyPaidTo(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Fortnightly,
		PaidTo:    dp("2026-11-15"),
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 100000}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-10"), 7)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	assertPeriod(t, got[0], "2026-11-16", "2026-11-29", 100000)
}

func TestPeriodsMonthlyNoDrift(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-01-31"),
		Frequency: Monthly,
		Changes:   []RentChange{{Effective: d("2026-01-31"), Amount: 200000}},
	}
	wantStarts := []string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30"}
	for n, want := range wantStarts {
		got := periodStart(terms, n)
		if !got.Equal(d(want)) {
			t.Errorf("periodStart(%d) = %s, want %s", n, got.Format("2006-01-02"), want)
		}
	}
}

func TestPeriodsMonthlyLeapYear(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2028-01-31"),
		Frequency: Monthly,
		Changes:   []RentChange{{Effective: d("2028-01-31"), Amount: 200000}},
	}
	got := periodStart(terms, 1)
	if !got.Equal(d("2028-02-29")) {
		t.Errorf("periodStart(1) = %s, want 2028-02-29", got.Format("2006-01-02"))
	}
}

func TestPeriodsWeeklyTruncatedByEnd(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		End:       dp("2026-11-11"),
		Frequency: Weekly,
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 50000}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-30"), 0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	assertPeriod(t, got[0], "2026-11-02", "2026-11-08", 50000)
	// 3 days x 50000/7 = 21428.57 -> 21429 (half away from zero).
	assertPeriod(t, got[1], "2026-11-09", "2026-11-11", 21429)
}

func TestPeriodsWeeklyRentChangeSplit(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Weekly,
		PaidTo:    dp("2026-11-08"),
		Changes: []RentChange{
			{Effective: d("2026-11-02"), Amount: 50000},
			{Effective: d("2026-11-12"), Amount: 56000},
		},
	}
	got := PeriodsToInvoice(terms, d("2026-11-09"), 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	// 3x50000/7 = 21428.57 -> 21429, 4x56000/7 = 32000 exact. Sum 53429.
	assertPeriod(t, got[0], "2026-11-09", "2026-11-15", 53429)
}

func TestPeriodsMonthlyChangeAtNextPeriodStartNoProration(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-01-31"),
		Frequency: Monthly,
		Changes: []RentChange{
			{Effective: d("2026-01-31"), Amount: 200000},
			{Effective: d("2026-02-28"), Amount: 220000},
		},
	}
	got := PeriodsToInvoice(terms, d("2026-02-28"), 0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	assertPeriod(t, got[0], "2026-01-31", "2026-02-27", 200000)
	assertPeriod(t, got[1], "2026-02-28", "2026-03-30", 220000)
}

func TestPeriodsChangeBeforePaidToDoesNotAffectReturnedPeriods(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Weekly,
		PaidTo:    dp("2026-11-22"),
		Changes: []RentChange{
			{Effective: d("2026-11-02"), Amount: 50000},
			{Effective: d("2026-11-09"), Amount: 56000},
		},
	}
	got := PeriodsToInvoice(terms, d("2026-11-23"), 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	assertPeriod(t, got[0], "2026-11-23", "2026-11-29", 56000)
}

func TestPeriodsPaidToThreeWeeksBack(t *testing.T) {
	start := d("2026-11-02")
	terms := LeaseTerms{
		Start:     start,
		Frequency: Weekly,
		PaidTo:    dp("2026-11-22"), // 3 periods already paid: 11-02, 11-09, 11-16.
		Changes:   []RentChange{{Effective: start, Amount: 50000}},
	}
	// "today" is the 5th period's start (11-30); 3 missed (11-23, 11-30 is
	// actually the 5th... use an asOf that lines up 3 missed + current).
	asOf := d("2026-12-14") // periods: ...11-23, 11-30, 12-07, 12-14 (current)
	got := PeriodsToInvoice(terms, asOf, 0)
	wantStarts := []string{"2026-11-23", "2026-11-30", "2026-12-07", "2026-12-14"}
	if len(got) != len(wantStarts) {
		t.Fatalf("len = %d, want %d", len(got), len(wantStarts))
	}
	for i, want := range wantStarts {
		if !got[i].Start.Equal(d(want)) {
			t.Errorf("period %d start = %s, want %s", i, got[i].Start.Format("2006-01-02"), want)
		}
		if got[i].Rent != 50000 {
			t.Errorf("period %d rent = %d, want 50000", i, got[i].Rent)
		}
	}
}

func TestPeriodsChargeActiveOnStart(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Weekly,
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 50000}},
		Charges:   []Charge{{Description: "Water", Amount: 2000, Start: d("2026-11-09")}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-09"), 0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if len(got[0].Charges) != 0 {
		t.Errorf("period 0 charges = %v, want none", got[0].Charges)
	}
	if len(got[1].Charges) != 1 || got[1].Charges[0].Description != "Water" || got[1].Charges[0].Amount != 2000 {
		t.Errorf("period 1 charges = %v, want [{Water 2000}]", got[1].Charges)
	}
}

func TestDailyRate(t *testing.T) {
	num, den := DailyRate(Monthly, 200000)
	if num != 2400000 || den != 365 {
		t.Errorf("DailyRate(monthly, 200000) = (%d, %d), want (2400000, 365)", num, den)
	}
}
