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
	// Periods after PaidTo are 11-23, 11-30, 12-07, 12-14, ...; asOf
	// 12-14 (with lead 0) lands on the 4th of those: 3 missed plus current.
	asOf := d("2026-12-14")
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

// TestPeriodsToInvoiceAsOfTimezoneNormalized covers the fix for asOf being
// compared as a raw instant: 2026-11-02T00:00:00+10:00 is
// 2026-11-01T14:00:00Z, strictly before the UTC-midnight period start it
// should match. PeriodsToInvoice must normalise asOf to its own calendar
// date (2026-11-02) before comparing, so the period starting that day is
// still returned.
func TestPeriodsToInvoiceAsOfTimezoneNormalized(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Weekly,
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 50000}},
	}
	brisbane := time.FixedZone("+10", 10*60*60)
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, brisbane)

	got := PeriodsToInvoice(terms, asOf, 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	assertPeriod(t, got[0], "2026-11-02", "2026-11-08", 50000)
}

// --- reviewer boundary probes ---

func TestPeriodsEndOnPeriodLastDay(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		End:       dp("2026-11-08"), // exactly the natural end of period 0.
		Frequency: Weekly,
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 50000}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-30"), 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (nothing after End)", len(got))
	}
	assertPeriod(t, got[0], "2026-11-02", "2026-11-08", 50000)
}

func TestPeriodsEndOnNextPeriodStart(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		End:       dp("2026-11-09"), // the next period's natural start.
		Frequency: Weekly,
		Changes:   []RentChange{{Effective: d("2026-11-02"), Amount: 50000}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-30"), 0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	assertPeriod(t, got[0], "2026-11-02", "2026-11-08", 50000)
	// 1 day x 50000/7 = 7142.86 -> 7143.
	assertPeriod(t, got[1], "2026-11-09", "2026-11-09", 7143)
}

func TestPeriodsChangeOnPeriodLastDay(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		Frequency: Weekly,
		Changes: []RentChange{
			{Effective: d("2026-11-02"), Amount: 50000},
			{Effective: d("2026-11-08"), Amount: 56000}, // the period's own last day.
		},
	}
	got := PeriodsToInvoice(terms, d("2026-11-02"), 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	// 6 days x 50000/7 = 42857.14 -> 42857, 1 day x 56000/7 = 8000 exact.
	assertPeriod(t, got[0], "2026-11-02", "2026-11-08", 50857)
}

func TestPeriodsTruncatedWithSplitAndChargeProration(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-11-02"),
		End:       dp("2026-11-05"),
		Frequency: Weekly,
		Changes: []RentChange{
			{Effective: d("2026-11-02"), Amount: 50000},
			{Effective: d("2026-11-04"), Amount: 56000},
		},
		Charges: []Charge{{Description: "Water", Amount: 2000, Start: d("2026-11-02")}},
	}
	got := PeriodsToInvoice(terms, d("2026-11-02"), 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	// 2 days x 50000/7 = 14285.7 -> 14286, 2 days x 56000/7 = 16000 exact.
	assertPeriod(t, got[0], "2026-11-02", "2026-11-05", 30286)
	// 4 days x 2000/7 = 1142.86 -> 1143.
	if len(got[0].Charges) != 1 || got[0].Charges[0].Amount != 1143 {
		t.Fatalf("charges = %v, want [{Water 1143}]", got[0].Charges)
	}
}

func TestPeriodsMonthlyEndEqualsNaturalEnd(t *testing.T) {
	terms := LeaseTerms{
		Start:     d("2026-03-31"),
		End:       dp("2026-04-29"), // exactly period 0's natural end.
		Frequency: Monthly,
		Changes:   []RentChange{{Effective: d("2026-03-31"), Amount: 200000}},
	}
	got := PeriodsToInvoice(terms, d("2026-03-31"), 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	assertPeriod(t, got[0], "2026-03-31", "2026-04-29", 200000)
}

func TestPeriodsMonthlyAnchors(t *testing.T) {
	tests := []struct {
		name       string
		start      string
		wantStarts []string
	}{
		{"2026-01-30 anchor", "2026-01-30", []string{"2026-01-30", "2026-02-28", "2026-03-30", "2026-04-30"}},
		{"2028-01-29 anchor (leap)", "2028-01-29", []string{"2028-01-29", "2028-02-29", "2028-03-29"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terms := LeaseTerms{Start: d(tt.start), Frequency: Monthly}
			for n, want := range tt.wantStarts {
				got := periodStart(terms, n)
				if !got.Equal(d(want)) {
					t.Errorf("periodStart(%d) = %s, want %s", n, got.Format("2006-01-02"), want)
				}
			}
		})
	}
}

func TestValidPeriodEnd(t *testing.T) {
	start := d("2026-11-02")
	tests := []struct {
		name      string
		candidate time.Time
		want      bool
	}{
		{"start-1 (nothing paid yet)", start.AddDate(0, 0, -1), true},
		{"first period's natural end", d("2026-11-08"), true},
		{"second period's natural end", d("2026-11-15"), true},
		{"misaligned mid-period date", d("2026-11-10"), false},
		{"before start-1", d("2026-10-01"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validPeriodEnd(start, Weekly, tt.candidate); got != tt.want {
				t.Errorf("validPeriodEnd(%s) = %v, want %v", tt.candidate.Format("2006-01-02"), got, tt.want)
			}
		})
	}
}
