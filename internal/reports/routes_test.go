package reports

import "testing"

// TestResolveRangeInclusiveDayLimit checks the 366-day range limit counts
// inclusively: 2026-07-01..2027-07-01 is exactly 366 days (the boundary,
// allowed), and 2026-07-01..2027-07-02 is 367 (one day over, rejected).
func TestResolveRangeInclusiveDayLimit(t *testing.T) {
	q := func(from, to string) map[string][]string {
		return map[string][]string{"from": {from}, "to": {to}}
	}

	if _, _, msg, ok := resolveRange(q("2026-07-01", "2027-07-01")); !ok {
		t.Errorf("366-day range rejected: %s", msg)
	}
	if _, _, msg, ok := resolveRange(q("2026-07-01", "2027-07-02")); ok {
		t.Errorf("367-day range accepted, want rejected")
	} else if msg == "" {
		t.Errorf("want a client-facing message on rejection")
	}
}

// TestResolveRangeToBeforeFrom checks to < from is rejected.
func TestResolveRangeToBeforeFrom(t *testing.T) {
	q := map[string][]string{"from": {"2026-07-10"}, "to": {"2026-07-01"}}
	if _, _, _, ok := resolveRange(q); ok {
		t.Errorf("to before from accepted, want rejected")
	}
}
