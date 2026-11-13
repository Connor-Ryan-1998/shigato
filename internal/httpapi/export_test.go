//go:build integration

package httpapi

// Re-exports for the integration tests in package httpapi_test, which live
// outside package httpapi so they can use internal/testkit (which imports
// httpapi).

type (
	ContactResponse     = contactResponse
	SettingsRequest     = settingsRequest
	InvoiceListResponse = invoiceListResponse
)
