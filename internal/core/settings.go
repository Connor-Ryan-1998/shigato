package core

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// OrgSettings is the full set of an org's editable settings: identity,
// GST/BAS configuration, bank/PayID payment details and rent-invoicing
// defaults.
type OrgSettings struct {
	Name            string
	ABN             string
	GSTRegistered   bool
	BASFrequency    string
	BankBSB         string
	BankAccount     string
	PayID           string
	InvoiceLeadDays int
	AutoSendRent    bool
}

var (
	bsbPattern      = regexp.MustCompile(`^\d{3}-?\d{3}$`)
	accountPattern  = regexp.MustCompile(`^\d{6,10}$`)
	auMobilePattern = regexp.MustCompile(`^(\+61|0)4\d{8}$`)
)

// ValidABN reports whether s is a valid Australian Business Number: 11
// digits once spaces are stripped, passing the ATO's modulus-89 checksum
// (subtract 1 from the first digit, multiply by the weights
// 10,1,3,5,7,9,11,13,15,17,19, and the sum must be divisible by 89). An
// empty ABN is allowed - it means "not provided".
func ValidABN(s string) bool {
	stripped := strings.ReplaceAll(s, " ", "")
	if stripped == "" {
		return true
	}
	if len(stripped) != 11 {
		return false
	}
	digits := make([]int, 11)
	for i, c := range stripped {
		if c < '0' || c > '9' {
			return false
		}
		digits[i] = int(c - '0')
	}
	digits[0]--
	weights := [11]int{10, 1, 3, 5, 7, 9, 11, 13, 15, 17, 19}
	sum := 0
	for i, w := range weights {
		sum += digits[i] * w
	}
	return sum%89 == 0
}

// validPayID reports whether s is a usable PayID: an email address, an AU
// mobile number (spaces allowed), or a valid ABN. Callers must check
// length and emptiness themselves.
func validPayID(s string) bool {
	if _, err := mail.ParseAddress(s); err == nil {
		return true
	}
	if auMobilePattern.MatchString(strings.ReplaceAll(s, " ", "")) {
		return true
	}
	return s != "" && ValidABN(s)
}

// validateSettings checks in against every org-settings rule and returns a
// normalized copy (bank_bsb as "ddd-ddd") ready to store. On failure it
// returns ErrInvalidInput wrapped with the offending field's name.
func validateSettings(in OrgSettings) (OrgSettings, error) {
	if in.Name == "" {
		return OrgSettings{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if !ValidABN(in.ABN) {
		return OrgSettings{}, fmt.Errorf("%w: invalid abn", ErrInvalidInput)
	}

	switch in.BASFrequency {
	case "", "quarterly", "monthly":
	default:
		return OrgSettings{}, fmt.Errorf("%w: invalid bas_frequency", ErrInvalidInput)
	}

	bsb := in.BankBSB
	if bsb != "" {
		if !bsbPattern.MatchString(bsb) {
			return OrgSettings{}, fmt.Errorf("%w: invalid bank_bsb", ErrInvalidInput)
		}
		digits := strings.ReplaceAll(bsb, "-", "")
		bsb = digits[:3] + "-" + digits[3:]
	}
	account := in.BankAccount
	if account != "" && !accountPattern.MatchString(account) {
		return OrgSettings{}, fmt.Errorf("%w: invalid bank_account", ErrInvalidInput)
	}
	if (bsb == "") != (account == "") {
		return OrgSettings{}, fmt.Errorf("%w: bank_bsb and bank_account must both be set or both be empty", ErrInvalidInput)
	}

	if in.PayID != "" {
		if len(in.PayID) > 256 {
			return OrgSettings{}, fmt.Errorf("%w: invalid payid", ErrInvalidInput)
		}
		if !validPayID(in.PayID) {
			return OrgSettings{}, fmt.Errorf("%w: invalid payid", ErrInvalidInput)
		}
	}

	if in.InvoiceLeadDays < 0 || in.InvoiceLeadDays > 14 {
		return OrgSettings{}, fmt.Errorf("%w: invalid invoice_lead_days", ErrInvalidInput)
	}

	out := in
	out.BankBSB = bsb
	out.BankAccount = account
	return out, nil
}

const settingsColumns = `name, coalesce(abn,''), gst_registered, coalesce(bas_frequency,''),
	coalesce(bank_bsb,''), coalesce(bank_account,''), coalesce(payid,''),
	invoice_lead_days, auto_send_rent`

func scanSettings(row pgx.Row) (OrgSettings, error) {
	var s OrgSettings
	err := row.Scan(&s.Name, &s.ABN, &s.GSTRegistered, &s.BASFrequency,
		&s.BankBSB, &s.BankAccount, &s.PayID, &s.InvoiceLeadDays, &s.AutoSendRent)
	return s, err
}

// GetSettings looks up orgID's settings. It returns ErrNotFound if no such
// org exists.
func GetSettings(ctx context.Context, q Querier, orgID int64) (OrgSettings, error) {
	row := q.QueryRow(ctx, `SELECT `+settingsColumns+` FROM orgs WHERE id = $1`, orgID)
	s, err := scanSettings(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgSettings{}, ErrNotFound
	}
	return s, err
}

// UpdateSettings validates in (returning ErrInvalidInput wrapped with the
// offending field's name on failure) and replaces orgID's settings
// wholesale. An empty optional field (abn, bank_bsb, bank_account, payid or
// bas_frequency) clears it (stored as SQL NULL), so billing.PayOptions
// keeps treating it as unset. It returns ErrNotFound if no such org exists.
func UpdateSettings(ctx context.Context, q Querier, orgID int64, in OrgSettings) (OrgSettings, error) {
	validated, err := validateSettings(in)
	if err != nil {
		return OrgSettings{}, err
	}

	row := q.QueryRow(ctx, `
		UPDATE orgs SET
			name = $1,
			abn = NULLIF($2, ''),
			gst_registered = $3,
			bas_frequency = NULLIF($4, ''),
			bank_bsb = NULLIF($5, ''),
			bank_account = NULLIF($6, ''),
			payid = NULLIF($7, ''),
			invoice_lead_days = $8,
			auto_send_rent = $9
		WHERE id = $10
		RETURNING `+settingsColumns,
		validated.Name, validated.ABN, validated.GSTRegistered, validated.BASFrequency,
		validated.BankBSB, validated.BankAccount, validated.PayID,
		validated.InvoiceLeadDays, validated.AutoSendRent, orgID,
	)
	out, err := scanSettings(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgSettings{}, ErrNotFound
	}
	return out, err
}
