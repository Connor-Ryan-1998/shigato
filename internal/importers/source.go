// Package importers turns a bank-exported file into a slice of parsed
// transaction lines. It knows nothing about orgs, the database, or HTTP: a
// Source just reads bytes and returns Lines (or ErrInvalidInput), so
// internal/recon can run it inside its own transaction without this
// package importing anything beyond internal/core.
package importers

import (
	"io"
	"time"

	"shigoto/internal/core"
)

// Line is one parsed transaction: a calendar date (UTC midnight), a signed
// amount in cents (money in positive, money out negative, per
// global-constraints.md), and a trimmed description.
type Line struct {
	PostedOn    time.Time
	Amount      core.Cents
	Description string
}

// Source parses a bank export into Lines. NewCSV is the only constructor
// today; a future source (e.g. OFX) would be another implementation of
// this same interface.
type Source interface {
	Lines(r io.Reader) ([]Line, error)
}
