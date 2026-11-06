package reports

import (
	"testing"

	"shigoto/internal/core"
)

// TestAllocateSplitsRemainderToLastLine checks the exact worked example from
// the task brief: a $50 payment against an invoice with two lines, one GST
// one FRE.
func TestAllocateSplitsRemainderToLastLine(t *testing.T) {
	lines := []AllocLine{
		{Amount: 10000, GST: 1000, Code: core.GST},
		{Amount: 4000, GST: 0, Code: core.FRE},
	}
	shares := Allocate(5000, lines)
	if len(shares) != 2 {
		t.Fatalf("len(shares) = %d, want 2", len(shares))
	}
	if shares[0].Incl != 3667 {
		t.Errorf("shares[0].Incl = %d, want 3667", shares[0].Incl)
	}
	if shares[0].GST != 333 {
		t.Errorf("shares[0].GST = %d, want 333", shares[0].GST)
	}
	if shares[0].Ex != 3667-333 {
		t.Errorf("shares[0].Ex = %d, want %d", shares[0].Ex, 3667-333)
	}
	if shares[1].Incl != 1333 {
		t.Errorf("shares[1].Incl = %d, want 1333 (remainder)", shares[1].Incl)
	}
	if shares[1].GST != 0 {
		t.Errorf("shares[1].GST = %d, want 0", shares[1].GST)
	}

	var sum core.Cents
	for _, s := range shares {
		sum += s.Incl
	}
	if sum != 5000 {
		t.Errorf("sum of shares = %d, want 5000", sum)
	}
}

// TestAllocateSingleLine checks that a single-line invoice's share is
// simply the payment itself (it's always both the first and the last
// line), with GST/Ex computed the normal way.
func TestAllocateSingleLine(t *testing.T) {
	lines := []AllocLine{{Amount: 900, GST: 100, Code: core.GST}}
	shares := Allocate(700, lines)
	if len(shares) != 1 {
		t.Fatalf("len(shares) = %d, want 1", len(shares))
	}
	if shares[0].Incl != 700 {
		t.Errorf("Incl = %d, want 700", shares[0].Incl)
	}
	wantGST := core.Cents(core.DivRound(700*100, 1000))
	if shares[0].GST != wantGST {
		t.Errorf("GST = %d, want %d", shares[0].GST, wantGST)
	}
	if shares[0].Ex != shares[0].Incl-shares[0].GST {
		t.Errorf("Ex = %d, want %d", shares[0].Ex, shares[0].Incl-shares[0].GST)
	}
}

// TestAllocateZeroTotalLine checks that a line with a zero incl-GST total
// (an edge case that shouldn't occur for a real invoice line, but must not
// divide by zero) gets a zero share without touching the other line's
// allocation.
func TestAllocateZeroTotalLine(t *testing.T) {
	lines := []AllocLine{
		{Amount: 0, GST: 0, Code: core.NT},
		{Amount: 1000, GST: 0, Code: core.FRE},
	}
	shares := Allocate(1000, lines)
	if len(shares) != 2 {
		t.Fatalf("len(shares) = %d, want 2", len(shares))
	}
	if shares[0].Incl != 0 || shares[0].GST != 0 || shares[0].Ex != 0 {
		t.Errorf("shares[0] = %+v, want all zero", shares[0])
	}
	if shares[1].Incl != 1000 {
		t.Errorf("shares[1].Incl = %d, want 1000 (remainder)", shares[1].Incl)
	}
}

// TestAllocateThreeLinesPartialPayment checks that three unevenly-sized
// lines, allocated a payment smaller than the invoice total, still sum to
// exactly the payment - the invariant the whole allocation rule exists to
// guarantee.
func TestAllocateThreeLinesPartialPayment(t *testing.T) {
	lines := []AllocLine{
		{Amount: 1000, GST: 0, Code: core.FRE},
		{Amount: 777, GST: 78, Code: core.GST},
		{Amount: 333, GST: 0, Code: core.FRE},
	}
	shares := Allocate(1500, lines)
	if len(shares) != 3 {
		t.Fatalf("len(shares) = %d, want 3", len(shares))
	}
	var sum core.Cents
	for _, s := range shares {
		sum += s.Incl
	}
	if sum != 1500 {
		t.Errorf("sum of shares = %d, want 1500", sum)
	}
}

// TestAllocateFullPaymentGivesExactLineTotals checks that allocating the
// full invoice total reproduces each line's own incl-GST total exactly, with
// no rounding error anywhere.
func TestAllocateFullPaymentGivesExactLineTotals(t *testing.T) {
	lines := []AllocLine{
		{Amount: 10000, GST: 1000, Code: core.GST},
		{Amount: 3333, GST: 0, Code: core.FRE},
		{Amount: 777, GST: 78, Code: core.GST},
	}
	var invoiceTotal core.Cents
	for _, l := range lines {
		invoiceTotal += l.Amount + l.GST
	}

	shares := Allocate(invoiceTotal, lines)
	for i, s := range shares {
		want := lines[i].Amount + lines[i].GST
		if s.Incl != want {
			t.Errorf("shares[%d].Incl = %d, want %d", i, s.Incl, want)
		}
		if s.GST != lines[i].GST {
			t.Errorf("shares[%d].GST = %d, want %d", i, s.GST, lines[i].GST)
		}
		if s.Ex != lines[i].Amount {
			t.Errorf("shares[%d].Ex = %d, want %d", i, s.Ex, lines[i].Amount)
		}
	}
}
