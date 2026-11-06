package reports

import (
	"encoding/json"
	"fmt"

	"shigoto/internal/core"
)

// BASFigures is a cash-basis Australian BAS, in cents.
type BASFigures struct {
	G1, G2, G3, G4, G10, G11 core.Cents
	A1, B1                   core.Cents
}

// basFiguresJSON is the wire shape for both BASFigures and BASDollars: the
// ATO's own field names, snake_case/lowercase so the API contract reads
// g1,g2,g3,g4,g10,g11,"1a","1b" (a struct field can't be named "1a" or
// "1b", hence the separate marshal-only type rather than json tags on
// BASFigures/BASDollars directly).
type basFiguresJSON struct {
	G1  int64 `json:"g1"`
	G2  int64 `json:"g2"`
	G3  int64 `json:"g3"`
	G4  int64 `json:"g4"`
	G10 int64 `json:"g10"`
	G11 int64 `json:"g11"`
	A1  int64 `json:"1a"`
	B1  int64 `json:"1b"`
}

// MarshalJSON renders f as {"g1":...,"g2":...,...,"1a":...,"1b":...}.
func (f BASFigures) MarshalJSON() ([]byte, error) {
	return json.Marshal(basFiguresJSON{
		G1: int64(f.G1), G2: int64(f.G2), G3: int64(f.G3), G4: int64(f.G4),
		G10: int64(f.G10), G11: int64(f.G11), A1: int64(f.A1), B1: int64(f.B1),
	})
}

// BASDollars is BASFigures truncated toward zero to whole dollars, the way
// the ATO's paper/online BAS form takes figures.
type BASDollars struct {
	G1, G2, G3, G4, G10, G11 int64
	A1, B1                   int64
}

// MarshalJSON renders d the same shape as BASFigures.MarshalJSON.
func (d BASDollars) MarshalJSON() ([]byte, error) {
	return json.Marshal(basFiguresJSON{
		G1: d.G1, G2: d.G2, G3: d.G3, G4: d.G4, G10: d.G10, G11: d.G11, A1: d.A1, B1: d.B1,
	})
}

// Dollars truncates each of f's figures toward zero to whole dollars.
func (f BASFigures) Dollars() BASDollars {
	return BASDollars{
		G1: int64(f.G1) / 100, G2: int64(f.G2) / 100, G3: int64(f.G3) / 100, G4: int64(f.G4) / 100,
		G10: int64(f.G10) / 100, G11: int64(f.G11) / 100,
		A1: int64(f.A1) / 100, B1: int64(f.B1) / 100,
	}
}

// basG1Income returns the income rows G1 (total sales) sums: codes GST,
// FRE and INP (every taxable classification except N-T, which is out of
// scope for GST reporting).
func basG1Income(income []IncomeRow) []IncomeRow {
	var out []IncomeRow
	for _, r := range income {
		switch r.Code {
		case core.GST, core.FRE, core.INP:
			out = append(out, r)
		}
	}
	return out
}

func basFREIncome(income []IncomeRow) []IncomeRow {
	var out []IncomeRow
	for _, r := range income {
		if r.Code == core.FRE {
			out = append(out, r)
		}
	}
	return out
}

func basINPIncome(income []IncomeRow) []IncomeRow {
	var out []IncomeRow
	for _, r := range income {
		if r.Code == core.INP {
			out = append(out, r)
		}
	}
	return out
}

// basCapitalExpense returns the capital expense lines G10 sums: capital
// lines whose code isn't N-T.
func basCapitalExpense(exp []ExpenseRow) []ExpenseRow {
	var out []ExpenseRow
	for _, r := range exp {
		if r.Capital && r.Code != core.NT {
			out = append(out, r)
		}
	}
	return out
}

// basNonCapitalExpense returns the non-capital expense lines G11 sums:
// non-capital lines whose code isn't N-T.
func basNonCapitalExpense(exp []ExpenseRow) []ExpenseRow {
	var out []ExpenseRow
	for _, r := range exp {
		if !r.Capital && r.Code != core.NT {
			out = append(out, r)
		}
	}
	return out
}

func sumIncl(rows []IncomeRow) core.Cents {
	var sum core.Cents
	for _, r := range rows {
		sum += r.Incl
	}
	return sum
}

func sumInclExpense(rows []ExpenseRow) core.Cents {
	var sum core.Cents
	for _, r := range rows {
		sum += r.Incl
	}
	return sum
}

func sumGST(rows []IncomeRow) core.Cents {
	var sum core.Cents
	for _, r := range rows {
		sum += r.GST
	}
	return sum
}

func sumGSTExpense(rows []ExpenseRow) core.Cents {
	var sum core.Cents
	for _, r := range rows {
		sum += r.GST
	}
	return sum
}

// BAS computes a cash-basis BAS from income and exp. G2 (export sales) is
// always 0: exports aren't tracked.
func BAS(income []IncomeRow, exp []ExpenseRow) BASFigures {
	return BASFigures{
		G1:  sumIncl(basG1Income(income)),
		G2:  0,
		G3:  sumIncl(basFREIncome(income)),
		G4:  sumIncl(basINPIncome(income)),
		G10: sumInclExpense(basCapitalExpense(exp)),
		G11: sumInclExpense(basNonCapitalExpense(exp)),
		A1:  sumGST(income),
		B1:  sumGSTExpense(exp),
	}
}

// BASDetail returns the exact rows BAS sums for field (one of "G1", "G3",
// "G4", "G10", "G11", "1A" or "1B"), using the same filter funcs BAS itself
// uses - the single source of truth that keeps a BAS figure tied to its
// drill-down rows exactly. Exactly one of the two returned slices is
// non-nil.
func BASDetail(income []IncomeRow, exp []ExpenseRow, field string) (incomeRows []IncomeRow, expenseRows []ExpenseRow, err error) {
	switch field {
	case "G1":
		return basG1Income(income), nil, nil
	case "G3":
		return basFREIncome(income), nil, nil
	case "G4":
		return basINPIncome(income), nil, nil
	case "G10":
		return nil, basCapitalExpense(exp), nil
	case "G11":
		return nil, basNonCapitalExpense(exp), nil
	case "1A":
		return income, nil, nil
	case "1B":
		return nil, exp, nil
	default:
		return nil, nil, fmt.Errorf("%w: unknown bas field %q", core.ErrInvalidInput, field)
	}
}
