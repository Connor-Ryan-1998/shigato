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
