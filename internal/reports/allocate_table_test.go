package reports

import (
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/testkit/table"
)

type allocateCase struct {
	Name    string `yaml:"name"`
	Payment int64  `yaml:"payment"`
	Lines   []struct {
		Amount int64  `yaml:"amount"`
		GST    int64  `yaml:"gst"`
		Code   string `yaml:"code"`
	} `yaml:"lines"`
	Want []struct {
		Incl int64 `yaml:"incl"`
		GST  int64 `yaml:"gst"`
		Ex   int64 `yaml:"ex"`
	} `yaml:"want"`
}

func TestAllocateTable(t *testing.T) {
	const path = "testdata/allocate.yaml"
	for _, c := range table.Load[allocateCase](t, path) {
		t.Run(c.Name, func(t *testing.T) {
			var lines []AllocLine
			for _, l := range c.Lines {
				line := AllocLine{Amount: core.Cents(l.Amount), GST: core.Cents(l.GST)}
				if l.Code != "" {
					code, err := core.ParseGSTCode(l.Code)
					if err != nil {
						t.Fatalf("%s: %q: %v", path, c.Name, err)
					}
					line.Code = code
				}
				lines = append(lines, line)
			}
			shares := Allocate(core.Cents(c.Payment), lines)
			if len(shares) != len(c.Want) {
				t.Fatalf("%s: %q: len(shares) = %d, want %d", path, c.Name, len(shares), len(c.Want))
			}
			var sum core.Cents
			for i, w := range c.Want {
				s := shares[i]
				sum += s.Incl
				if s.Incl != core.Cents(w.Incl) || s.GST != core.Cents(w.GST) || s.Ex != core.Cents(w.Ex) {
					t.Errorf("%s: %q: share %d = incl %d gst %d ex %d, want %d, %d, %d",
						path, c.Name, i, s.Incl, s.GST, s.Ex, w.Incl, w.GST, w.Ex)
				}
			}
			if sum != core.Cents(c.Payment) {
				t.Errorf("%s: %q: shares sum to %d, want payment %d", path, c.Name, sum, c.Payment)
			}
		})
	}
}
