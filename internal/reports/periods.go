package reports

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// fyQuarterPattern matches a period string like "FY2027-Q1".
var fyQuarterPattern = regexp.MustCompile(`^FY(\d{4})-Q([1-4])$`)

// FYQuarter parses a financial-year quarter string such as "FY2027-Q1" into
// its inclusive calendar date range, given the org's fy_start_month (1-12,
// default 7 for the Australian financial year). "FY<year>" names the
// financial year ending in <year> (e.g. with fyStartMonth 7, FY2027 runs
// 2026-07-01 to 2027-06-30); Q1 covers the FY's first three months.
func FYQuarter(s string, fyStartMonth int) (from, to time.Time, err error) {
	if fyStartMonth < 1 || fyStartMonth > 12 {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid fy_start_month %d: must be 1-12", fyStartMonth)
	}
	m := fyQuarterPattern.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid period %q: want FY<year>-Q<1-4>", s)
	}
	year, _ := strconv.Atoi(m[1])
	quarter, _ := strconv.Atoi(m[2])

	// The FY named "year" starts in the previous calendar year unless the
	// FY itself starts in January, in which case it coincides with "year".
	startYear := year - 1
	if fyStartMonth == 1 {
		startYear = year
	}

	y, mo := addMonths(startYear, fyStartMonth, (quarter-1)*3)
	from = time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
	endY, endMo := addMonths(y, mo, 2)
	to = lastDayOfMonth(endY, endMo)
	return from, to, nil
}

// addMonths returns (year, month) n calendar months after (year, month); n
// must be >= 0.
func addMonths(year, month, n int) (int, int) {
	idx := month - 1 + n
	return year + idx/12, idx%12 + 1
}

// lastDayOfMonth returns the last day of (year, month) at UTC midnight.
func lastDayOfMonth(year, month int) time.Time {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC)
}

// FinancialYear returns the inclusive calendar date range of the financial
// year named by its end year fyEndYear (FY2027 = 2027), given the org's
// fy_start_month (1-12). As in FYQuarter, an FY starting in January
// coincides with its calendar year.
func FinancialYear(fyEndYear, fyStartMonth int) (from, to time.Time, err error) {
	from, _, err = FYQuarter(fmt.Sprintf("FY%04d-Q1", fyEndYear), fyStartMonth)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from, from.AddDate(1, 0, -1), nil
}
