package module

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"shigoto/internal/core"
)

// TestWriteCoreErrorMapsErrors covers the HTTP status WriteCoreError gives
// each core sentinel error, including core.ErrForbidden (403), added for
// timesheets' ownership checks.
func TestWriteCoreErrorMapsErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", core.ErrNotFound, 404},
		{"forbidden", core.ErrForbidden, 403},
		{"invalid input", fmt.Errorf("%w: bad", core.ErrInvalidInput), 422},
		{"invalid state", core.ErrInvalidState, 409},
		{"unknown", fmt.Errorf("boom"), 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			WriteCoreError(rr, tc.err)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

// TestWriteCoreErrorInvalidStateMessages covers the 409 body text:
// ErrInvalidState wrapped with internal detail (e.g. a payments provider's
// error message) must still render the fixed generic "invalid state" text
// rather than leak that detail to an API client, while
// ErrInvoiceHasPayments - also an ErrInvalidState - keeps its own exact
// message.
func TestWriteCoreErrorInvalidStateMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"bare ErrInvalidState", core.ErrInvalidState, "invalid state"},
		{"wrapped with internal detail", fmt.Errorf("stripe: invoice 7 has no due date: %w", core.ErrInvalidState), "invalid state"},
		{"invoice has payments", core.ErrInvoiceHasPayments, "invoice has payments; record a credit note instead"},
		{"public error", &core.PublicError{Err: core.ErrInvalidState, Msg: "payout has unmatched transactions: txn_1"}, "payout has unmatched transactions: txn_1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			WriteCoreError(rr, tc.err)
			if rr.Code != 409 {
				t.Fatalf("status = %d, want 409", rr.Code)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body.Error != tc.want {
				t.Fatalf("error = %q, want %q", body.Error, tc.want)
			}
		})
	}
}
