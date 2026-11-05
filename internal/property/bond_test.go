package property

import (
	"testing"

	"shigoto/internal/core"
)

func TestBondStatusDueWithNoInvoice(t *testing.T) {
	if got := BondStatus(Lease{}, nil, nil); got != "due" {
		t.Errorf("BondStatus = %q, want due", got)
	}
}

func TestBondStatusHeldWhenPaid(t *testing.T) {
	inv := &core.Invoice{Status: "paid"}
	paidOn := d("2026-11-01")
	if got := BondStatus(Lease{}, inv, &paidOn); got != "held" {
		t.Errorf("BondStatus = %q, want held", got)
	}
}

func TestBondStatusLodgedWhenRefSet(t *testing.T) {
	l := Lease{RTALodgementRef: "QLD-12345"}
	inv := &core.Invoice{Status: "paid"}
	paidOn := d("2026-11-01")
	if got := BondStatus(l, inv, &paidOn); got != "lodged" {
		t.Errorf("BondStatus = %q, want lodged", got)
	}
}

func TestBondWarningsTenDayBoundary(t *testing.T) {
	paidOn := d("2026-11-01")

	if got := BondWarnings("held", &paidOn, d("2026-11-11")); len(got) != 0 {
		t.Errorf("warnings at exactly 10 days = %v, want none", got)
	}
	got := BondWarnings("held", &paidOn, d("2026-11-12"))
	if len(got) != 1 || got[0] != "bond not lodged with RTA within 10 days" {
		t.Errorf("warnings at 11 days = %v, want the 10-day warning", got)
	}
}

func TestBondWarningsOnlyWhenHeld(t *testing.T) {
	paidOn := d("2026-11-01")
	if got := BondWarnings("due", &paidOn, d("2026-11-20")); len(got) != 0 {
		t.Errorf("warnings for due status = %v, want none", got)
	}
	if got := BondWarnings("lodged", &paidOn, d("2026-11-20")); len(got) != 0 {
		t.Errorf("warnings for lodged status = %v, want none", got)
	}
}
