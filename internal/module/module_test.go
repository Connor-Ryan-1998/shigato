package module

import (
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
