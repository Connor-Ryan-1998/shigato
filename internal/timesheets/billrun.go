package timesheets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/billing"
	"shigoto/internal/core"
)

// formatRate formats cents as "d.dd" (e.g. 12000 -> "120.00"), for the rate
// shown in a billing-run line's description.
func formatRate(c core.Cents) string {
	return fmt.Sprintf("%d.%02d", int64(c)/100, int64(c)%100)
}

// formatMinutes formats a minute count as "<H>h <MM>m" (e.g. 125 -> "2h
// 05m"), for the hours shown in a billing-run line's description.
func formatMinutes(totalMinutes int) string {
	return fmt.Sprintf("%dh %02dm", totalMinutes/60, totalMinutes%60)
}

// LineFor builds the draft invoice line for one task's unbilled time: a
// single line at QuantityMilli 1000 whose UnitAmount is the task's effective
// rate applied to totalMinutes (DivRound(totalMinutes*rate, 60), so 50
// minutes at $120/h bills exactly $100.00, never rounded through
// milli-hours). GSTCode is p's gst_code when the org is GST-registered,
// otherwise FRE (the GST-on-consulting-lines rule). from/to are the billing
// run's inclusive range, shown in the description.
func LineFor(p Project, t Task, totalMinutes int, from, to time.Time, gstRegistered bool) core.NewLine {
	rate := t.EffectiveRate(p)
	unitAmount := core.Cents(core.DivRound(int64(totalMinutes)*int64(rate), 60))

	gstCode := core.FRE
	if gstRegistered {
		gstCode = p.GSTCode
	}

	description := fmt.Sprintf("%s – %s: %s @ $%s/h (%s – %s)",
		p.Name, t.Name, formatMinutes(totalMinutes), formatRate(rate),
		from.Format(dateLayout), to.Format(dateLayout))

	return core.NewLine{
		Description:   description,
		QuantityMilli: 1000,
		UnitAmount:    unitAmount,
		GSTCode:       string(gstCode),
		ProjectID:     &p.ID,
	}
}

// RunInput describes one timesheet billing run: bill ClientContactID's
// unbilled time logged in [From, To] onto a new draft invoice, optionally
// issuing it immediately.
type RunInput struct {
	ClientContactID int64
	From, To        time.Time
	DueInDays       int
	Issue           bool
}

// RunResult summarises a completed billing run: the invoice it created, how
// many time entries it billed and their total minutes.
type RunResult struct {
	Invoice      core.Invoice
	EntryCount   int
	TotalMinutes int
}

// taskGroup accumulates one task's unbilled entries within a billing run.
type taskGroup struct {
	task         Task
	project      Project
	entryIDs     []int64
	totalMinutes int
}

// RunBilling bills orgID's client ClientContactID for its unbilled time
// entries in [From, To]: one draft invoice line per task with unbilled
// time, ordered by project name then task name. It validates From <= To,
// DueInDays between 0 and 90, and that ClientContactID is a "client"
// contact in orgID, otherwise core.ErrInvalidInput.
//
// The unbilled entries are selected and locked (FOR UPDATE, entries rows
// only - no join in the locked query) in the same transaction as the draft
// invoice and the entries' invoice_line_id stamp, so a concurrent run for
// the same client/range either waits and then sees nothing left to bill, or
// proceeds against entries this run didn't touch. No matching entries
// returns core.ErrInvalidInput ("no unbilled time").
//
// If in.Issue is set, b.Issue is called once the billing transaction has
// committed; a failure there is returned as-is and the draft invoice (with
// its entries already locked to it) is left in place for a retry.
func RunBilling(ctx context.Context, b *billing.Service, orgID int64, orgSlug string, in RunInput) (RunResult, error) {
	if in.From.After(in.To) {
		return RunResult{}, fmt.Errorf("%w: from must not be after to", core.ErrInvalidInput)
	}
	if in.DueInDays < 0 || in.DueInDays > 90 {
		return RunResult{}, fmt.Errorf("%w: due_in_days must be between 0 and 90", core.ErrInvalidInput)
	}

	contact, err := core.GetContact(ctx, b.Pool, orgID, in.ClientContactID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return RunResult{}, fmt.Errorf("%w: client_contact_id not found", core.ErrInvalidInput)
		}
		return RunResult{}, err
	}
	if contact.Kind != "client" {
		return RunResult{}, fmt.Errorf("%w: client_contact_id is not a client", core.ErrInvalidInput)
	}

	settings, err := core.GetSettings(ctx, b.Pool, orgID)
	if err != nil {
		return RunResult{}, err
	}

	var result RunResult
	var invoiceID int64
	err = runBillingTx(ctx, b.Pool, func(tx pgx.Tx) error {
		groups, err := lockUnbilledEntries(ctx, tx, orgID, in)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return fmt.Errorf("%w: no unbilled time", core.ErrInvalidInput)
		}

		sort.Slice(groups, func(i, j int) bool {
			if groups[i].project.Name != groups[j].project.Name {
				return groups[i].project.Name < groups[j].project.Name
			}
			return groups[i].task.Name < groups[j].task.Name
		})

		lines := make([]core.NewLine, len(groups))
		for i, g := range groups {
			lines[i] = LineFor(g.project, g.task, g.totalMinutes, in.From, in.To, settings.GSTRegistered)
			result.EntryCount += len(g.entryIDs)
			result.TotalMinutes += g.totalMinutes
		}

		inv, err := core.CreateDraftInvoice(ctx, tx, orgID, core.NewInvoice{
			ContactID: in.ClientContactID,
			Source:    "timesheet",
			DueInDays: in.DueInDays,
			Lines:     lines,
		})
		if err != nil {
			return err
		}
		if len(inv.Lines) != len(groups) {
			return fmt.Errorf("timesheets: created invoice has %d lines, want %d", len(inv.Lines), len(groups))
		}

		for i, g := range groups {
			if _, err := tx.Exec(ctx,
				`UPDATE time_entries SET invoice_line_id = $1 WHERE org_id = $2 AND id = ANY($3)`,
				inv.Lines[i].ID, orgID, g.entryIDs,
			); err != nil {
				return err
			}
		}

		result.Invoice = inv
		invoiceID = inv.ID
		return nil
	})
	if err != nil {
		return RunResult{}, err
	}

	if in.Issue {
		inv, err := b.Issue(ctx, orgID, orgSlug, invoiceID)
		if err != nil {
			return RunResult{}, err
		}
		result.Invoice = inv
	}

	return result, nil
}

// lockUnbilledEntries selects and locks (FOR UPDATE) orgID's unbilled time
// entries for in.ClientContactID's projects within [in.From, in.To], and
// groups them by task. The locked query touches time_entries rows only; the
// task/project filter is a subquery, never a join on the locked rows, so
// Postgres only has to lock what it's actually billing, and re-checks
// invoice_line_id IS NULL for any row it had to wait on.
func lockUnbilledEntries(ctx context.Context, tx pgx.Tx, orgID int64, in RunInput) ([]taskGroup, error) {
	rows, err := tx.Query(ctx, `
		SELECT e.id, e.task_id, e.minutes FROM time_entries e
		WHERE e.org_id=$1 AND e.invoice_line_id IS NULL AND e.work_date BETWEEN $2 AND $3
		AND e.task_id IN (
			SELECT t.id FROM project_tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE t.org_id=$1 AND p.org_id=$1 AND p.client_contact_id=$4
		)
		ORDER BY e.task_id, e.id FOR UPDATE`,
		orgID, in.From, in.To, in.ClientContactID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byTask := map[int64]*taskGroup{}
	var order []int64
	for rows.Next() {
		var entryID, taskID int64
		var minutes int
		if err := rows.Scan(&entryID, &taskID, &minutes); err != nil {
			return nil, err
		}
		g, ok := byTask[taskID]
		if !ok {
			g = &taskGroup{}
			byTask[taskID] = g
			order = append(order, taskID)
		}
		g.entryIDs = append(g.entryIDs, entryID)
		g.totalMinutes += minutes
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}

	projects := map[int64]Project{}
	groups := make([]taskGroup, 0, len(order))
	for _, taskID := range order {
		g := byTask[taskID]
		task, err := GetTask(ctx, tx, orgID, taskID)
		if err != nil {
			return nil, err
		}
		project, ok := projects[task.ProjectID]
		if !ok {
			project, err = GetProject(ctx, tx, orgID, task.ProjectID)
			if err != nil {
				return nil, err
			}
			projects[task.ProjectID] = project
		}
		g.task = task
		g.project = project
		groups = append(groups, *g)
	}
	return groups, nil
}

// runBillingTx runs fn in a new transaction on pool, committing on success
// and rolling back (a no-op once committed) otherwise. Mirrors billing's own
// unexported runInTx; duplicated here since Service doesn't expose it.
func runBillingTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
