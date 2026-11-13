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
