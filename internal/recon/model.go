// Package recon implements the reconciliation module: bank accounts, their
// idempotent CSV imports, and the resulting statement lines. internal/importers
// does the actual file parsing; this package is the DB-backed layer on top
// of it (store functions plus HTTP routes), following the same shape as
// internal/timesheets and internal/property.
package recon

import (
	"time"

	"shigoto/internal/core"
	"shigoto/internal/importers"
)

// BankAccount is one of an org's bank accounts, along with the CSV mapping
// used to import its statements.
type BankAccount struct {
	ID         int64
	OrgID      int64
	Name       string
	CSVMapping importers.CSVMapping
	CreatedAt  time.Time
}

// ImportResult summarises one CSV import: how many rows it parsed in
// total, and how many of those were new (not already present for this
// bank account, by dedupe key).
type ImportResult struct {
	RowsTotal int
	RowsNew   int
}

// BankLine is one statement line, produced by an import and pending
// reconciliation against a payment.
type BankLine struct {
	ID            int64
	OrgID         int64
	BankAccountID int64
	ImportID      int64
	PostedOn      time.Time
	Amount        core.Cents
	Description   string
	Status        string
	IgnoreReason  *string
}

// BankLineFilter narrows ListBankLines. A nil field means "don't filter on
// this".
type BankLineFilter struct {
	BankAccountID *int64
	Status        *string
	From          *time.Time
	To            *time.Time
}
