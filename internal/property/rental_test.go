package property

import (
	"reflect"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/reports"
	"shigoto/internal/testkit/table"
)

type item21Case struct {
	Name   string `yaml:"name"`
	FY     int    `yaml:"fy"`
	Income []struct {
		Category string `yaml:"category"`
		Ex       int64  `yaml:"ex"`
	} `yaml:"income"`
	Bills []struct {
		Category string `yaml:"category"`
		Label    string `yaml:"label"`
		Capital  bool   `yaml:"capital"`
		Ex       int64  `yaml:"ex"`
	} `yaml:"bills"`
	Depreciation []struct {
		Type    string `yaml:"type"`
		Amounts []struct {
			FY     int   `yaml:"fy"`
			Amount int64 `yaml:"amount"`
		} `yaml:"amounts"`
	} `yaml:"depreciation"`
	Want struct {
		GrossRent   int64            `yaml:"gross_rent"`
		OtherIncome int64            `yaml:"other_income"`
		Deductions  map[string]int64 `yaml:"deductions"`
		Warnings    []string         `yaml:"warnings"`
	} `yaml:"want"`
}

func TestItem21Table(t *testing.T) {
	const path = "testdata/item21.yaml"
	for _, c := range table.Load[item21Case](t, path) {
		t.Run(c.Name, func(t *testing.T) {
			var income []reports.IncomeRow
			for _, r := range c.Income {
				income = append(income, reports.IncomeRow{Category: r.Category, Ex: core.Cents(r.Ex)})
			}
			var bills []BillLine
			for _, b := range c.Bills {
				line := BillLine{Category: b.Category, Capital: b.Capital, Ex: core.Cents(b.Ex)}
				if b.Label != "" {
					l := core.Item21Label(b.Label)
					line.Label = &l
				}
				bills = append(bills, line)
			}
			var dep []DepreciationItem
			for _, d := range c.Depreciation {
				item := DepreciationItem{Type: d.Type}
				for _, a := range d.Amounts {
					item.Amounts = append(item.Amounts, DepreciationAmount{FY: a.FY, Amount: core.Cents(a.Amount)})
				}
				dep = append(dep, item)
			}

			got := Item21(income, bills, dep, c.FY)
			if int64(got.GrossRent) != c.Want.GrossRent || int64(got.OtherIncome) != c.Want.OtherIncome {
				t.Errorf("%s: %q: income = %d/%d, want %d/%d", path, c.Name, got.GrossRent, got.OtherIncome, c.Want.GrossRent, c.Want.OtherIncome)
			}
			gotDed := map[string]int64{}
			for l, v := range got.Deductions {
				gotDed[string(l)] = int64(v)
			}
			want := c.Want.Deductions
			if want == nil {
				want = map[string]int64{}
			}
			if !reflect.DeepEqual(gotDed, want) {
				t.Errorf("%s: %q: deductions = %v, want %v", path, c.Name, gotDed, want)
			}
			if len(got.Warnings) != len(c.Want.Warnings) || (len(got.Warnings) > 0 && !reflect.DeepEqual(got.Warnings, c.Want.Warnings)) {
				t.Errorf("%s: %q: warnings = %q, want %q", path, c.Name, got.Warnings, c.Want.Warnings)
			}
		})
	}
}
