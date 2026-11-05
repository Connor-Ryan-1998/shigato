package core

import (
	"testing"
)

func TestLineAmount(t *testing.T) {
	tests := []struct {
		name          string
		quantityMilli int64
		unit          Cents
		want          Cents
	}{
		{"1.5h @ $120.00", 1500, 12000, 18000},
		{"0.333 → 0", 333, 1, 0},
		{"0.5 → 1 (half away from zero)", 500, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LineAmount(tt.quantityMilli, tt.unit)
			if got != tt.want {
				t.Errorf("LineAmount(%d, %d) = %d, want %d", tt.quantityMilli, tt.unit, got, tt.want)
			}
		})
	}
}

func TestGSTOnExclusive(t *testing.T) {
	tests := []struct {
		name   string
		amount Cents
		code   GSTCode
		want   Cents
	}{
		{"18000 @ GST", 18000, GST, 1800},
		{"5 @ GST → 1", 5, GST, 1},
		{"4 @ GST", 4, GST, 0},
		{"50000 @ INP", 50000, INP, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GSTOnExclusive(tt.amount, tt.code)
			if got != tt.want {
				t.Errorf("GSTOnExclusive(%d, %q) = %d, want %d", tt.amount, tt.code, got, tt.want)
			}
		})
	}
}

func TestGSTInInclusive(t *testing.T) {
	tests := []struct {
		name  string
		total Cents
		code  GSTCode
		want  Cents
	}{
		{"1100 @ GST", 1100, GST, 100},
		{"1000 @ GST → 91", 1000, GST, 91},
		{"1000 @ FRE", 1000, FRE, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GSTInInclusive(tt.total, tt.code)
			if got != tt.want {
				t.Errorf("GSTInInclusive(%d, %q) = %d, want %d", tt.total, tt.code, got, tt.want)
			}
		})
	}
}

func TestTotals(t *testing.T) {
	tests := []struct {
		name         string
		lines        []Line
		wantSubtotal Cents
		wantGST      Cents
		wantTotal    Cents
	}{
		{"per-line rounding: 1+1, not round(1.0)", []Line{
			{1000, 5, GST},
			{1000, 5, GST},
		}, 10, 2, 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSubtotal, gotGST, gotTotal := Totals(tt.lines)
			if gotSubtotal != tt.wantSubtotal || gotGST != tt.wantGST || gotTotal != tt.wantTotal {
				t.Errorf("Totals(...) = (%d, %d, %d), want (%d, %d, %d)",
					gotSubtotal, gotGST, gotTotal,
					tt.wantSubtotal, tt.wantGST, tt.wantTotal)
			}
		})
	}
}

func TestParseGSTCode(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    GSTCode
		wantErr bool
	}{
		{"valid GST", "GST", GST, false},
		{"valid FRE", "FRE", FRE, false},
		{"valid INP", "INP", INP, false},
		{"valid N-T", "N-T", NT, false},
		{"invalid GST-ish", "GST-ish", "", true},
		{"invalid empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGSTCode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseGSTCode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseGSTCode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
