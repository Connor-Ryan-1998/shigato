package property

import (
	"slices"
	"testing"
)

// TestToTermsFixedTermEndContinuesPeriodic covers Ruling 22: end_date ends
// only the fixed term; the tenancy (and its rent) continues periodic until
// a termination_date is recorded.
func TestToTermsFixedTermEndContinuesPeriodic(t *testing.T) {
	l := Lease{
		StartDate: d("2026-11-02"), EndDate: dp("2026-11-15"), Frequency: Weekly,
		RentChanges: []LeaseRentChange{{EffectiveDate: d("2026-11-02"), Amount: 50000}},
	}

	terms := l.ToTerms()
	if terms.End != nil {
		t.Fatalf("End = %v, want nil (end_date must not stop rent)", terms.End)
	}
	periods := PeriodsToInvoice(terms, d("2026-11-23"), 0)
	if len(periods) != 4 {
		t.Fatalf("len = %d, want 4 periods running past end_date", len(periods))
	}
	assertPeriod(t, periods[3], "2026-11-23", "2026-11-29", 50000)

	l.TerminationDate = dp("2026-11-20")
	if terms := l.ToTerms(); terms.End == nil || !terms.End.Equal(d("2026-11-20")) {
		t.Fatalf("End = %v, want termination_date 2026-11-20", terms.End)
	}
}

func TestLeaseWarningsFixedTermEnded(t *testing.T) {
	l := Lease{StartDate: d("2026-11-02"), EndDate: dp("2026-11-15")}

	if got := leaseWarnings(l, d("2026-11-15")); len(got) != 0 {
		t.Fatalf("on end_date: warnings = %v, want none", got)
	}
	if got := leaseWarnings(l, d("2026-11-16")); !slices.Equal(got, []string{warnFixedTermEnded}) {
		t.Fatalf("after end_date: warnings = %v, want [%q]", got, warnFixedTermEnded)
	}
	l.TerminationDate = dp("2026-11-30")
	if got := leaseWarnings(l, d("2026-11-16")); len(got) != 0 {
		t.Fatalf("terminated: warnings = %v, want none", got)
	}
}
