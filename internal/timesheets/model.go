// Package timesheets implements the timesheets module: billable projects,
// their tasks, and (in a later task) logged time entries billed onto
// invoices.
package timesheets

import (
	"time"

	"shigoto/internal/core"
)

// Project statuses. Pending and in-progress projects accept time; the
// others are closed.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"
)

// Billing methods. Time on a fixed-price project is tracked but never
// billed by the billing run.
const (
	BillingTM    = "tm"
	BillingFixed = "fixed"
)

// Project is a billable engagement for one client contact. Active is
// derived from Status by the database (pending or in progress) and is
// read-only.
type Project struct {
	ID              int64
	OrgID           int64
	ClientContactID int64
	Name            string
	HourlyRate      core.Cents
	GSTCode         core.GSTCode
	Status          string
	Description     string
	StartDate       *time.Time
	EndDate         *time.Time
	BillingMethod   string
	Active          bool
}

// Task is one task within a Project. HourlyRate, when set, overrides the
// project's rate for time logged against this task.
type Task struct {
	ID         int64
	OrgID      int64
	ProjectID  int64
	Name       string
	HourlyRate *core.Cents
	Active     bool
}

// EffectiveRate returns t's billing rate: its own override if set,
// otherwise p's rate. p must be the Project t belongs to.
func (t Task) EffectiveRate(p Project) core.Cents {
	if t.HourlyRate != nil {
		return *t.HourlyRate
	}
	return p.HourlyRate
}
