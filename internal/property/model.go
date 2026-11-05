// Package property implements the property/lease module: property and lease
// CRUD, rent changes, charges, lease termination, and the pure rent-period
// maths (periods.go) that later tasks use to generate rent invoices.
package property

import (
	"time"

	"shigoto/internal/core"
)

// Frequency is how often rent is charged.
type Frequency string

const (
	Weekly      Frequency = "weekly"
	Fortnightly Frequency = "fortnightly"
	Monthly     Frequency = "monthly"
)

// Property is a rental property owned by an org.
type Property struct {
	ID          int64
	OrgID       int64
	Name        string
	Address     string
	Kind        string
	Residential bool
}

// LeaseTenant is one tenant on a lease; exactly one tenant per lease is
// primary.
type LeaseTenant struct {
	ContactID int64
	IsPrimary bool
}

// LeaseRentChange is a persisted row of lease_rent_changes. The row dated
// the lease's start_date is the initial rent, not a "change".
type LeaseRentChange struct {
	ID            int64
	EffectiveDate time.Time
	Amount        core.Cents
}

// LeaseCharge is a persisted row of lease_charges: a recurring per-period
// charge (e.g. water) alongside rent.
type LeaseCharge struct {
	ID          int64
	Description string
	Amount      core.Cents
	StartDate   time.Time
	EndDate     *time.Time
}

// Lease is a persisted lease and its tenants, rent changes and charges.
type Lease struct {
	ID              int64
	OrgID           int64
	PropertyID      int64
	StartDate       time.Time
	EndDate         *time.Time
	TerminationDate *time.Time
	Frequency       Frequency
	PaidToDate      *time.Time
	OpeningArrears  core.Cents
	// OpeningArrearsInvoiceID is the draft invoice CreateLease raises for
	// OpeningArrears, when that's positive.
	OpeningArrearsInvoiceID *int64
	BondAmount              core.Cents
	BondInvoiceID           *int64
	RTALodgementRef         string
	BondLodgedOn            *time.Time
	Tenants                 []LeaseTenant
	RentChanges             []LeaseRentChange
	Charges                 []LeaseCharge
}

// --- pure rent-period types (periods.go) ---

// RentChange is one point at which the rent changes, for the pure period
// maths: the lease's initial rent is itself a RentChange dated start_date.
type RentChange struct {
	Effective time.Time
	Amount    core.Cents
}

// Charge is a recurring per-period charge alongside rent, for the pure
// period maths.
type Charge struct {
	Description string
	Amount      core.Cents
	Start       time.Time
	End         *time.Time
}

// LeaseTerms is the pure, DB-free view of a lease periods.go computes
// against. All dates are UTC midnight. End is termination_date once
// loaded from the DB (end_date doesn't stop rent). Changes must be sorted ascending by Effective,
// with Changes[0].Effective <= Start.
type LeaseTerms struct {
	Start     time.Time
	End       *time.Time
	Frequency Frequency
	PaidTo    *time.Time
	Changes   []RentChange
	Charges   []Charge
}

// ChargeLine is one charge applied to a Period.
type ChargeLine struct {
	Description string
	Amount      core.Cents
}

// Period is one rent period to invoice: inclusive UTC-midnight dates, the
// rent due (already prorated/split as needed) and any charges active on
// Start.
type Period struct {
	Start, End time.Time
	Rent       core.Cents
	Charges    []ChargeLine
}
