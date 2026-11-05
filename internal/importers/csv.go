package importers

import (
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"shigoto/internal/core"
)

// CSVMapping describes how to read one bank's CSV export: which columns
// hold the date, amount and description, and the date's layout. Exactly
// one of AmountColumn (a single signed column) or DebitColumn+CreditColumn
// (two unsigned columns) must be set.
type CSVMapping struct {
	HasHeader         bool   `json:"has_header"`
	DateColumn        int    `json:"date_column"`
	DateFormat        string `json:"date_format"`
	AmountColumn      *int   `json:"amount_column,omitempty"`
	DebitColumn       *int   `json:"debit_column,omitempty"`
	CreditColumn      *int   `json:"credit_column,omitempty"`
	DescriptionColumn int    `json:"description_column"`
}

// knownDateFormats are the layouts a CSVMapping.DateFormat may name.
var knownDateFormats = map[string]bool{
	"02/01/2006": true,
	"2006-01-02": true,
	"2 Jan 2006": true,
}

// Validate checks m is internally consistent: exactly one amount style
// (a single signed column, or a debit+credit pair) and a known date
// format.
func (m CSVMapping) Validate() error {
	hasAmount := m.AmountColumn != nil
	hasDebitCredit := m.DebitColumn != nil && m.CreditColumn != nil
	if m.AmountColumn != nil && (m.DebitColumn != nil || m.CreditColumn != nil) {
		return fmt.Errorf("%w: csv_mapping must not set amount_column together with debit_column/credit_column", core.ErrInvalidInput)
	}
	if hasAmount == hasDebitCredit {
		return fmt.Errorf("%w: csv_mapping must set exactly one of amount_column or debit_column+credit_column", core.ErrInvalidInput)
	}
	if !knownDateFormats[m.DateFormat] {
		return fmt.Errorf("%w: unknown date_format %q", core.ErrInvalidInput, m.DateFormat)
	}
	return nil
}

// minColumns returns the fewest columns a row needs to satisfy m.
func (m CSVMapping) minColumns() int {
	max := m.DateColumn
	if m.DescriptionColumn > max {
		max = m.DescriptionColumn
	}
	for _, c := range []*int{m.AmountColumn, m.DebitColumn, m.CreditColumn} {
		if c != nil && *c > max {
			max = *c
		}
	}
	return max + 1
}

// csvSource is the Source implementation NewCSV builds.
type csvSource struct{ m CSVMapping }

// NewCSV returns a Source that reads delimited text per m.
func NewCSV(m CSVMapping) Source { return csvSource{m: m} }

// Preset returns the built-in CSVMapping for a named bank export, or
// false if name isn't recognized.
func Preset(name string) (CSVMapping, bool) {
	switch name {
	case "commbank":
		amountColumn := 1
		return CSVMapping{
			HasHeader:         false,
			DateColumn:        0,
			DateFormat:        "02/01/2006",
			AmountColumn:      &amountColumn,
			DescriptionColumn: 2,
		}, true
	default:
		return CSVMapping{}, false
	}
}

// Lines parses r per s.m. Rows with fewer columns than the mapping needs,
// an unparseable date, or an unparseable amount all give ErrInvalidInput
// naming the offending row's 1-based number (counting the header row, if
// any). A row whose total amount is zero is skipped.
func (s csvSource) Lines(r io.Reader) ([]Line, error) {
	if err := s.m.Validate(); err != nil {
		return nil, err
	}

	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", core.ErrInvalidInput, err)
	}

	start := 0
	if s.m.HasHeader {
		start = 1
	}
	need := s.m.minColumns()

	var lines []Line
	for i := start; i < len(rows); i++ {
		rowNum := i + 1
		row := rows[i]
		if len(row) < need {
			return nil, fmt.Errorf("%w: row %d has fewer columns than the mapping needs", core.ErrInvalidInput, rowNum)
		}

		dateStr := strings.TrimSpace(row[s.m.DateColumn])
		parsed, err := time.ParseInLocation(s.m.DateFormat, dateStr, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("%w: row %d: invalid date %q", core.ErrInvalidInput, rowNum, dateStr)
		}
		y, mo, d := parsed.Date()
		postedOn := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)

		var amount core.Cents
		if s.m.AmountColumn != nil {
			amount, err = parseAmountCents(row[*s.m.AmountColumn])
			if err != nil {
				return nil, fmt.Errorf("%w: row %d: %v", core.ErrInvalidInput, rowNum, err)
			}
		} else {
			debit, err := parseAmountCents(row[*s.m.DebitColumn])
			if err != nil {
				return nil, fmt.Errorf("%w: row %d: %v", core.ErrInvalidInput, rowNum, err)
			}
			credit, err := parseAmountCents(row[*s.m.CreditColumn])
			if err != nil {
				return nil, fmt.Errorf("%w: row %d: %v", core.ErrInvalidInput, rowNum, err)
			}
			amount = credit - debit
		}
		if amount == 0 {
			continue
		}

		lines = append(lines, Line{
			PostedOn:    postedOn,
			Amount:      amount,
			Description: strings.TrimSpace(row[s.m.DescriptionColumn]),
		})
	}
	return lines, nil
}

// parseAmountCents parses one amount cell into cents, using integer
// arithmetic only (no float parsing, so there's no rounding error to
// worry about). A blank cell is 0. Accepted forms: "1234.56",
// "1,234.56", "-12.30", "(12.30)" (parentheses mean negative), "+5.00",
// "$12.30", with at most two decimal places.
func parseAmountCents(raw string) (core.Cents, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}

	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg = true
		s = s[1 : len(s)-1]
	}
	switch {
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	case strings.HasPrefix(s, "-"):
		neg = !neg
		s = s[1:]
	}
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, fmt.Errorf("%w: empty amount", core.ErrInvalidInput)
	}

	intPart, fracPart := s, ""
	if idx := strings.IndexByte(s, '.'); idx >= 0 {
		intPart, fracPart = s[:idx], s[idx+1:]
		if len(fracPart) > 2 {
			return 0, fmt.Errorf("%w: amount %q has more than two decimal places", core.ErrInvalidInput, raw)
		}
	}
	if intPart == "" {
		intPart = "0"
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}

	iv, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid amount %q", core.ErrInvalidInput, raw)
	}
	fv, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid amount %q", core.ErrInvalidInput, raw)
	}

	cents := iv*100 + fv
	if neg {
		cents = -cents
	}
	return core.Cents(cents), nil
}

// DedupeKeys returns one sha256 dedupe key per line in lines, in order.
// The key is sha256 of "<date YYYY-MM-DD>|<cents>|<description>|<n>",
// where n is the 0-based occurrence index of that exact (date, cents,
// description) triple within lines - so two genuinely identical
// transactions on the same statement line (e.g. two identical $5 coffees)
// still get distinct, stable keys.
func DedupeKeys(lines []Line) [][]byte {
	seen := map[string]int{}
	keys := make([][]byte, len(lines))
	for i, l := range lines {
		triple := fmt.Sprintf("%s|%d|%s", l.PostedOn.Format("2006-01-02"), int64(l.Amount), l.Description)
		n := seen[triple]
		seen[triple] = n + 1
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%d", l.PostedOn.Format("2006-01-02"), int64(l.Amount), l.Description, n)))
		keys[i] = sum[:]
	}
	return keys
}
