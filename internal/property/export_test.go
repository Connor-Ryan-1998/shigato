//go:build integration

package property

import "time"

// Re-exports for the integration tests in package property_test, which live
// outside package property so they can use internal/testkit (which imports
// property).

var (
	CreateBondDraft = createBondDraft
	Today           = today
)

const (
	DateLayout                            = dateLayout
	WarnMoreThanOneIncreaseWithin12Months = warnMoreThanOneIncreaseWithin12Months
)

// SetClockNow replaces clockNow with f and returns a func restoring it.
func SetClockNow(f func() time.Time) (restore func()) {
	orig := clockNow
	clockNow = f
	return func() { clockNow = orig }
}
