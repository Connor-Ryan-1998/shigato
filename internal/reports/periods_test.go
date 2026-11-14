package reports

import (
	"testing"
	"time"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %q: %v", s, err)
	}
	return d
}

func TestFYQuarter(t *testing.T) {
	cases := []struct {
		period   string
		from, to string
	}{
		{"FY2027-Q1", "2026-07-01", "2026-09-30"},
		{"FY2027-Q3", "2027-01-01", "2027-03-31"},
		{"FY2027-Q2", "2026-10-01", "2026-12-31"},
		{"FY2027-Q4", "2027-04-01", "2027-06-30"},
	}
	for _, c := range cases {
		from, to, err := FYQuarter(c.period, 7)
		if err != nil {
			t.Fatalf("FYQuarter(%q, 7): %v", c.period, err)
		}
		if !from.Equal(mustDate(t, c.from)) {
			t.Errorf("FYQuarter(%q, 7).from = %s, want %s", c.period, from.Format("2006-01-02"), c.from)
		}
		if !to.Equal(mustDate(t, c.to)) {
			t.Errorf("FYQuarter(%q, 7).to = %s, want %s", c.period, to.Format("2006-01-02"), c.to)
		}
	}
}

func TestFYQuarterInvalid(t *testing.T) {
	for _, bad := range []string{"", "2027-Q1", "FY2027", "FY2027-Q5", "FY27-Q1", "FY2027-Q0"} {
		if _, _, err := FYQuarter(bad, 7); err == nil {
			t.Errorf("FYQuarter(%q, 7): want error, got nil", bad)
		}
	}
}

// TestFYQuarterOtherFYStartMonths checks FYQuarter against fy_start_months
// other than the Australian default of 7: 1 (a calendar-year FY, where
// FY<year> is simply that year) and 4 (a UK-style April start).
func TestFYQuarterOtherFYStartMonths(t *testing.T) {
	cases := []struct {
		fyStartMonth int
		period       string
		from, to     string
	}{
		{1, "FY2027-Q1", "2027-01-01", "2027-03-31"},
		{4, "FY2027-Q1", "2026-04-01", "2026-06-30"},
	}
	for _, c := range cases {
		from, to, err := FYQuarter(c.period, c.fyStartMonth)
		if err != nil {
			t.Fatalf("FYQuarter(%q, %d): %v", c.period, c.fyStartMonth, err)
		}
		if !from.Equal(mustDate(t, c.from)) {
			t.Errorf("FYQuarter(%q, %d).from = %s, want %s", c.period, c.fyStartMonth, from.Format("2006-01-02"), c.from)
		}
		if !to.Equal(mustDate(t, c.to)) {
			t.Errorf("FYQuarter(%q, %d).to = %s, want %s", c.period, c.fyStartMonth, to.Format("2006-01-02"), c.to)
		}
	}
}

// TestFYQuarterInvalidFYStartMonth checks FYQuarter rejects an
// out-of-range fy_start_month instead of silently misbehaving.
func TestFYQuarterInvalidFYStartMonth(t *testing.T) {
	for _, m := range []int{0, 13, -1} {
		if _, _, err := FYQuarter("FY2027-Q1", m); err == nil {
			t.Errorf("FYQuarter(FY2027-Q1, %d): want error, got nil", m)
		}
	}
}

func TestFinancialYear(t *testing.T) {
	cases := []struct {
		fy, month int
		from, to  string
	}{
		{2027, 7, "2026-07-01", "2027-06-30"},
		{2027, 1, "2027-01-01", "2027-12-31"},
		{2028, 4, "2027-04-01", "2028-03-31"},
	}
	for _, c := range cases {
		from, to, err := FinancialYear(c.fy, c.month)
		if err != nil {
			t.Fatalf("FinancialYear(%d, %d): %v", c.fy, c.month, err)
		}
		if !from.Equal(mustDate(t, c.from)) || !to.Equal(mustDate(t, c.to)) {
			t.Errorf("FinancialYear(%d, %d) = %s..%s, want %s..%s", c.fy, c.month, from.Format("2006-01-02"), to.Format("2006-01-02"), c.from, c.to)
		}
	}
	if _, _, err := FinancialYear(2027, 13); err == nil {
		t.Error("FinancialYear(2027, 13): want error")
	}
}
