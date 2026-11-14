package timesheets

import (
	"context"

	"shigoto/internal/billing"
	"shigoto/internal/core"
)

// SetIssueForTest replaces how RunBilling issues each created invoice,
// returning a func that restores the real one. Timesheet invoices have no
// natural issue failure, so tests use this to drive one.
func SetIssueForTest(f func(*billing.Service, context.Context, int64, string, int64) (core.Invoice, error)) (restore func()) {
	prev := issueInvoice
	issueInvoice = f
	return func() { issueInvoice = prev }
}
