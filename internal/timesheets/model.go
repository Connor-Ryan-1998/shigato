// Package timesheets implements the timesheets module: billable projects,
// their tasks, and (in a later task) logged time entries billed onto
// invoices.
package timesheets

import "shigoto/internal/core"

// Project is a billable engagement for one client contact.
type Project struct {
	ID              int64
	OrgID           int64
	ClientContactID int64
	Name            string
	HourlyRate      core.Cents
	GSTCode         core.GSTCode
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
