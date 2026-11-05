package importers_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/importers"
)

func mustPreset(t *testing.T, name string) importers.CSVMapping {
	t.Helper()
	m, ok := importers.Preset(name)
	if !ok {
		t.Fatalf("Preset(%q): not found", name)
	}
	return m
}

func TestCommBankPresetParsesFixture(t *testing.T) {
	csv := "01/03/2026,-45.50,Coffee Shop\n" +
		"02/03/2026,1200.00,Salary\n" +
		"03/03/2026,-12.99,  Book Store  \n"

	lines, err := importers.NewCSV(mustPreset(t, "commbank")).Lines(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}

	wantDates := []time.Time{
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
	}
	wantAmounts := []core.Cents{-4550, 120000, -1299}
	wantDescriptions := []string{"Coffee Shop", "Salary", "Book Store"}

	for i, l := range lines {
		if !l.PostedOn.Equal(wantDates[i]) {
			t.Errorf("line %d: PostedOn = %v, want %v", i, l.PostedOn, wantDates[i])
		}
		if l.Amount != wantAmounts[i] {
			t.Errorf("line %d: Amount = %d, want %d", i, l.Amount, wantAmounts[i])
		}
		if l.Description != wantDescriptions[i] {
			t.Errorf("line %d: Description = %q, want %q", i, l.Description, wantDescriptions[i])
		}
	}
}

func TestCommBankPresetStripsLeadingBOM(t *testing.T) {
	csv := "\xEF\xBB\xBF" + "01/03/2026,-45.50,Coffee Shop\n" +
		"02/03/2026,1200.00,Salary\n" +
		"03/03/2026,-12.99,Book Store\n"

	lines, err := importers.NewCSV(mustPreset(t, "commbank")).Lines(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	if lines[0].Amount != -4550 || lines[0].Description != "Coffee Shop" {
		t.Fatalf("line 0 = %+v, want amount -4550, description Coffee Shop", lines[0])
	}
}

func TestCommBankPresetCRLFLineEndings(t *testing.T) {
	csv := "01/03/2026,-45.50,Coffee Shop\r\n" +
		"02/03/2026,1200.00,Salary\r\n" +
		"03/03/2026,-12.99,Book Store\r\n"

	lines, err := importers.NewCSV(mustPreset(t, "commbank")).Lines(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
}

func TestCommBankPresetTrailingBlankLines(t *testing.T) {
	csv := "01/03/2026,-45.50,Coffee Shop\n" +
		"02/03/2026,1200.00,Salary\n" +
		"\n\n"

	lines, err := importers.NewCSV(mustPreset(t, "commbank")).Lines(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2 (trailing blank lines ignored)", len(lines))
	}
}

func TestDebitCreditMapping(t *testing.T) {
	debitCol, creditCol := 1, 2
	m := importers.CSVMapping{
		HasHeader:         true,
		DateColumn:        0,
		DateFormat:        "2006-01-02",
		DebitColumn:       &debitCol,
		CreditColumn:      &creditCol,
		DescriptionColumn: 3,
	}
	csv := "date,debit,credit,description\n2026-03-01,12.30,,Some purchase\n"

	lines, err := importers.NewCSV(m).Lines(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	if lines[0].Amount != -1230 {
		t.Fatalf("Amount = %d, want -1230", lines[0].Amount)
	}
}

func TestAmountFormats(t *testing.T) {
	cases := []struct {
		in   string
		want core.Cents
	}{
		{"1234.56", 123456},
		{"1,234.56", 123456},
		{"-12.30", -1230},
		{"(12.30)", -1230},
		{"+5.00", 500},
		{"$12.30", 1230},
		{"", 0},
	}
	amountCol := 1
	m := importers.CSVMapping{
		DateColumn: 0, DateFormat: "2006-01-02", AmountColumn: &amountCol, DescriptionColumn: 2,
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			// A zero amount row is skipped entirely, so pair it with a
			// nonzero sibling row to still observe the parse. Quote the
			// amount field since some cases (e.g. "1,234.56") contain a
			// literal comma that would otherwise split into two fields.
			csv := `2026-03-01,"` + tc.in + `",desc` + "\n2026-03-02,1.00,other\n"
			lines, err := importers.NewCSV(m).Lines(strings.NewReader(csv))
			if err != nil {
				t.Fatalf("Lines: %v", err)
			}
			if tc.want == 0 {
				if len(lines) != 1 {
					t.Fatalf("len(lines) = %d, want 1 (zero-amount row skipped)", len(lines))
				}
				return
			}
			if len(lines) != 2 {
				t.Fatalf("len(lines) = %d, want 2", len(lines))
			}
			if lines[0].Amount != tc.want {
				t.Fatalf("Amount = %d, want %d", lines[0].Amount, tc.want)
			}
		})
	}
}

func TestAmountTooManyDecimalPlacesNamesRow(t *testing.T) {
	amountCol := 1
	m := importers.CSVMapping{
		HasHeader: true, DateColumn: 0, DateFormat: "2006-01-02", AmountColumn: &amountCol, DescriptionColumn: 2,
	}
	csv := "date,amount,description\n2026-03-01,1.00,ok\n2026-03-02,12.345,bad\n"

	_, err := importers.NewCSV(m).Lines(strings.NewReader(csv))
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "row 3") {
		t.Fatalf("err = %v, want it to name row 3 (header is row 1)", err)
	}
}

func TestShortRowNamesRow(t *testing.T) {
	amountCol := 1
	m := importers.CSVMapping{
		DateColumn: 0, DateFormat: "2006-01-02", AmountColumn: &amountCol, DescriptionColumn: 2,
	}
	csv := "2026-03-01,1.00,ok\n2026-03-02,2.00\n"

	_, err := importers.NewCSV(m).Lines(strings.NewReader(csv))
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("err = %v, want it to name row 2", err)
	}
}

func TestDedupeKeysDistinctForIdenticalTriplesAndStableAcrossRuns(t *testing.T) {
	lines := []importers.Line{
		{PostedOn: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Amount: 500, Description: "Coffee"},
		{PostedOn: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Amount: 500, Description: "Coffee"},
	}
	keys1 := importers.DedupeKeys(lines)
	if len(keys1) != 2 {
		t.Fatalf("len(keys1) = %d, want 2", len(keys1))
	}
	if string(keys1[0]) == string(keys1[1]) {
		t.Fatalf("identical triples got the same dedupe key")
	}

	keys2 := importers.DedupeKeys(lines)
	if string(keys1[0]) != string(keys2[0]) || string(keys1[1]) != string(keys2[1]) {
		t.Fatalf("DedupeKeys is not stable across runs of the same input")
	}
}

func TestValidateRejectsBothAmountStyles(t *testing.T) {
	amountCol, debitCol, creditCol := 1, 1, 2
	m := importers.CSVMapping{
		DateColumn: 0, DateFormat: "2006-01-02",
		AmountColumn: &amountCol, DebitColumn: &debitCol, CreditColumn: &creditCol,
		DescriptionColumn: 3,
	}
	if err := m.Validate(); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestValidateRejectsNeitherAmountStyle(t *testing.T) {
	m := importers.CSVMapping{DateColumn: 0, DateFormat: "2006-01-02", DescriptionColumn: 1}
	if err := m.Validate(); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestValidateRejectsUnknownDateFormat(t *testing.T) {
	amountCol := 1
	m := importers.CSVMapping{DateColumn: 0, DateFormat: "01-02-2006", AmountColumn: &amountCol, DescriptionColumn: 2}
	if err := m.Validate(); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}
