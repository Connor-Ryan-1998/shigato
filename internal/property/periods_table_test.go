package property

import (
	"testing"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/testkit/table"
)

type periodsCase struct {
	Name  string      `yaml:"name"`
	Terms termsYAML   `yaml:"terms"`
	AsOf  string      `yaml:"as_of"`
	Lead  int         `yaml:"lead"`
	Want  []periodOut `yaml:"want"`
}

type termsYAML struct {
	Start     string `yaml:"start"`
	End       string `yaml:"end"`
	Frequency string `yaml:"frequency"`
	PaidTo    string `yaml:"paid_to"`
	Changes   []struct {
		Effective string `yaml:"effective"`
		Amount    int64  `yaml:"amount"`
	} `yaml:"changes"`
	Charges []struct {
		Description string `yaml:"description"`
		Amount      int64  `yaml:"amount"`
		Start       string `yaml:"start"`
	} `yaml:"charges"`
}

type periodOut struct {
	Start   string `yaml:"start"`
	End     string `yaml:"end"`
	Rent    int64  `yaml:"rent"`
	Charges []struct {
		Description string `yaml:"description"`
		Amount      int64  `yaml:"amount"`
	} `yaml:"charges"`
}

type periodStartsCase struct {
	Name       string   `yaml:"name"`
	Start      string   `yaml:"start"`
	WantStarts []string `yaml:"want_starts"`
}

func TestPeriodsToInvoiceTable(t *testing.T) {
	const path = "testdata/periods.yaml"
	for _, c := range table.Load[periodsCase](t, path) {
		t.Run(c.Name, func(t *testing.T) {
			got := PeriodsToInvoice(c.Terms.build(t), mustDay(t, c.AsOf), c.Lead)
			if len(got) != len(c.Want) {
				t.Fatalf("%s: %q: len = %d, want %d", path, c.Name, len(got), len(c.Want))
			}
			for i, w := range c.Want {
				p := got[i]
				if !p.Start.Equal(mustDay(t, w.Start)) || !p.End.Equal(mustDay(t, w.End)) {
					t.Errorf("%s: %q: period %d = [%s..%s], want [%s..%s]", path, c.Name, i,
						p.Start.Format(time.DateOnly), p.End.Format(time.DateOnly), w.Start, w.End)
				}
				if p.Rent != core.Cents(w.Rent) {
					t.Errorf("%s: %q: period %d rent = %d, want %d", path, c.Name, i, p.Rent, w.Rent)
				}
				if len(p.Charges) != len(w.Charges) {
					t.Errorf("%s: %q: period %d charges = %v, want %v", path, c.Name, i, p.Charges, w.Charges)
					continue
				}
				for j, wc := range w.Charges {
					if p.Charges[j].Description != wc.Description || p.Charges[j].Amount != core.Cents(wc.Amount) {
						t.Errorf("%s: %q: period %d charge %d = %v, want {%s %d}", path, c.Name, i, j,
							p.Charges[j], wc.Description, wc.Amount)
					}
				}
			}
		})
	}
}

func TestPeriodStartTable(t *testing.T) {
	const path = "testdata/period_starts.yaml"
	for _, c := range table.Load[periodStartsCase](t, path) {
		t.Run(c.Name, func(t *testing.T) {
			terms := LeaseTerms{Start: mustDay(t, c.Start), Frequency: Monthly}
			for n, want := range c.WantStarts {
				if got := periodStart(terms, n); !got.Equal(mustDay(t, want)) {
					t.Errorf("%s: %q: periodStart(%d) = %s, want %s", path, c.Name, n, got.Format(time.DateOnly), want)
				}
			}
		})
	}
}

func (y termsYAML) build(t *testing.T) LeaseTerms {
	t.Helper()
	terms := LeaseTerms{
		Start:     mustDay(t, y.Start),
		End:       optDay(t, y.End),
		Frequency: mustFrequency(t, y.Frequency),
		PaidTo:    optDay(t, y.PaidTo),
	}
	for _, ch := range y.Changes {
		terms.Changes = append(terms.Changes, RentChange{Effective: mustDay(t, ch.Effective), Amount: core.Cents(ch.Amount)})
	}
	for _, ch := range y.Charges {
		terms.Charges = append(terms.Charges, Charge{Description: ch.Description, Amount: core.Cents(ch.Amount), Start: mustDay(t, ch.Start)})
	}
	return terms
}

func mustFrequency(t *testing.T, s string) Frequency {
	t.Helper()
	switch f := Frequency(s); f {
	case Weekly, Fortnightly, Monthly:
		return f
	}
	t.Fatalf("unknown frequency %q", s)
	return ""
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation(time.DateOnly, s, time.UTC)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}
	return d
}

func optDay(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	d := mustDay(t, s)
	return &d
}
