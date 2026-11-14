package timesheets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

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

// issueInvoice issues one invoice a billing run created. It is a variable
// only so tests can force an issue failure (see export_test.go): a
// timesheet invoice has no natural way to fail issuing.
var issueInvoice = (*billing.Service).Issue

// ErrPreviewChanged is RunBilling's error when the selected unbilled time
// no longer matches what the caller previewed: time was logged, edited or
// billed since. Nothing is written; the caller must refresh and review.
var ErrPreviewChanged error = &core.PublicError{Err: core.ErrInvalidState, Msg: "billing preview changed; refresh and review"}

// Split values for RunInput.Split.
const (
	SplitCombined = "combined"
	SplitProject  = "project"
)

// PreviewInput selects the unbilled time PreviewBilling summarises:
// ClientContactID's time-and-materials time logged in [From, To].
type PreviewInput struct {
	ClientContactID int64
	From, To        time.Time
}

// PreviewTask is one task's unbilled time and what billing it would add
// to an invoice: Amount is its line amount (ex GST) and GSTAmount the GST
// on it, computed exactly as the billing run's invoice line will be.
type PreviewTask struct {
	TaskID    int64
	TaskName  string
	Minutes   int
	Amount    core.Cents
	GSTAmount core.Cents
}

// PreviewProject groups a project's unbilled tasks, sorted by task name.
type PreviewProject struct {
	ProjectID   int64
	ProjectName string
	Tasks       []PreviewTask
}

// Preview is the unbilled time a billing run could bill, projects sorted
// by name.
type Preview struct {
	Projects []PreviewProject
}

// TaskSelection is one task the caller chose to bill, echoing the minutes
// and amount its preview showed so RunBilling can refuse a stale review.
type TaskSelection struct {
	TaskID  int64
	Minutes int
	Amount  core.Cents
}

// RunInput describes one timesheet billing run: bill the selected tasks of
// ClientContactID's unbilled time logged in [From, To], as one invoice
// (Split "combined") or one per project (Split "project"), optionally
// issuing each immediately.
type RunInput struct {
	ClientContactID int64
	From, To        time.Time
	DueInDays       int
	Issue           bool
	Split           string
	Tasks           []TaskSelection
}

// RunInvoice is one invoice a billing run created. IssueErr is the error
// issuing it returned, when RunInput.Issue was set and issuing failed; the
// invoice is then still the committed draft.
type RunInvoice struct {
	Invoice  core.Invoice
	IssueErr error
}

// RunResult summarises a completed billing run: the invoices it created
// (in project-name order), how many time entries it billed and their total
// minutes.
type RunResult struct {
	Invoices     []RunInvoice
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

// checkBillingClient validates a billing range and that clientID is a "client"
// contact in orgID, returning core.ErrInvalidInput otherwise.
func checkBillingClient(ctx context.Context, q core.Querier, orgID, clientID int64, from, to time.Time) error {
	if from.After(to) {
		return fmt.Errorf("%w: from must not be after to", core.ErrInvalidInput)
	}
	contact, err := core.GetContact(ctx, q, orgID, clientID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("%w: client_contact_id not found", core.ErrInvalidInput)
		}
		return err
	}
	if contact.Kind != "client" {
		return fmt.Errorf("%w: client_contact_id is not a client", core.ErrInvalidInput)
	}
	return nil
}

// sortGroups orders groups by project name then task name (ids break
// ties), the order a billing run's lines and invoices follow.
func sortGroups(groups []taskGroup) {
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.project.Name != b.project.Name {
			return a.project.Name < b.project.Name
		}
		if a.project.ID != b.project.ID {
			return a.project.ID < b.project.ID
		}
		if a.task.Name != b.task.Name {
			return a.task.Name < b.task.Name
		}
		return a.task.ID < b.task.ID
	})
}

// PreviewBilling summarises in.ClientContactID's unbilled
// time-and-materials time in [From, To] by project and task, pricing each
// task exactly as RunBilling's invoice line would. It locks nothing: a
// preview is advisory, and RunBilling re-checks the selection under lock.
func PreviewBilling(ctx context.Context, q core.Querier, orgID int64, in PreviewInput) (Preview, error) {
	if err := checkBillingClient(ctx, q, orgID, in.ClientContactID, in.From, in.To); err != nil {
		return Preview{}, err
	}
	settings, err := core.GetSettings(ctx, q, orgID)
	if err != nil {
		return Preview{}, err
	}
	groups, err := unbilledGroups(ctx, q, orgID, in.ClientContactID, in.From, in.To, nil, false)
	if err != nil {
		return Preview{}, err
	}
	sortGroups(groups)

	preview := Preview{Projects: []PreviewProject{}}
	for _, g := range groups {
		line := LineFor(g.project, g.task, g.totalMinutes, in.From, in.To, settings.GSTRegistered)
		code, err := core.ParseGSTCode(line.GSTCode)
		if err != nil {
			return Preview{}, err
		}
		n := len(preview.Projects)
		if n == 0 || preview.Projects[n-1].ProjectID != g.project.ID {
			preview.Projects = append(preview.Projects, PreviewProject{ProjectID: g.project.ID, ProjectName: g.project.Name})
			n++
		}
		preview.Projects[n-1].Tasks = append(preview.Projects[n-1].Tasks, PreviewTask{
			TaskID: g.task.ID, TaskName: g.task.Name, Minutes: g.totalMinutes,
			Amount: line.UnitAmount, GSTAmount: core.GSTOnExclusive(line.UnitAmount, code),
		})
	}
	return preview, nil
}

// checkSelection validates a run's split and task selection.
func checkSelection(in RunInput) error {
	if in.Split != SplitCombined && in.Split != SplitProject {
		return fmt.Errorf("%w: split must be combined or project", core.ErrInvalidInput)
	}
	if len(in.Tasks) == 0 {
		return fmt.Errorf("%w: tasks must not be empty", core.ErrInvalidInput)
	}
	seen := map[int64]bool{}
	for _, t := range in.Tasks {
		if seen[t.TaskID] {
			return fmt.Errorf("%w: task %d is selected twice", core.ErrInvalidInput, t.TaskID)
		}
		seen[t.TaskID] = true
	}
	return nil
}

// RunBilling bills the selected tasks of orgID's client ClientContactID's
// unbilled time entries in [From, To]: one draft invoice line per task,
// ordered by project name then task name, on one invoice (Split
// "combined") or one invoice per project (Split "project"). It validates
// From <= To, DueInDays between 0 and 90, the split, a non-empty selection
// without duplicates, and that ClientContactID is a "client" contact in
// orgID, otherwise core.ErrInvalidInput.
//
// The selected tasks' unbilled entries are locked (FOR UPDATE, entries
// rows only - no join in the locked query) in the same transaction as the
// draft invoices and the entries' invoice_line_id stamps. Each selected
// task's locked minutes and line amount must equal what the caller
// previewed; any difference - including a task with nothing left to bill,
// or one that isn't this client's - returns ErrPreviewChanged with nothing
// written. That also makes a retried run whose response was lost safe:
// its entries are already billed, so it changes nothing.
//
// If in.Issue is set, each invoice is issued once the transaction has
// committed. An issue failure is reported on that invoice's RunInvoice and
// does not stop the others: every draft and its locked entries survive,
// so the caller can find and retry each one.
func RunBilling(ctx context.Context, b *billing.Service, orgID int64, orgSlug string, in RunInput) (RunResult, error) {
	if err := checkBillingClient(ctx, b.Pool, orgID, in.ClientContactID, in.From, in.To); err != nil {
		return RunResult{}, err
	}
	if in.DueInDays < 0 || in.DueInDays > 90 {
		return RunResult{}, fmt.Errorf("%w: due_in_days must be between 0 and 90", core.ErrInvalidInput)
	}
	if err := checkSelection(in); err != nil {
		return RunResult{}, err
	}

	settings, err := core.GetSettings(ctx, b.Pool, orgID)
	if err != nil {
		return RunResult{}, err
	}

	taskIDs := make([]int64, len(in.Tasks))
	for i, t := range in.Tasks {
		taskIDs[i] = t.TaskID
	}

	var result RunResult
	err = billing.RunInTx(ctx, b.Pool, func(tx pgx.Tx) error {
		groups, err := unbilledGroups(ctx, tx, orgID, in.ClientContactID, in.From, in.To, taskIDs, true)
		if err != nil {
			return err
		}
		if len(groups) != len(in.Tasks) {
			return ErrPreviewChanged
		}
		byTask := map[int64]taskGroup{}
		for _, g := range groups {
			byTask[g.task.ID] = g
		}
		for _, sel := range in.Tasks {
			g, ok := byTask[sel.TaskID]
			if !ok || g.totalMinutes != sel.Minutes {
				return ErrPreviewChanged
			}
			if LineFor(g.project, g.task, g.totalMinutes, in.From, in.To, settings.GSTRegistered).UnitAmount != sel.Amount {
				return ErrPreviewChanged
			}
		}

		sortGroups(groups)
		var batches [][]taskGroup
		for _, g := range groups {
			n := len(batches)
			if n == 0 || (in.Split == SplitProject && batches[n-1][0].project.ID != g.project.ID) {
				batches = append(batches, nil)
				n++
			}
			batches[n-1] = append(batches[n-1], g)
		}

		for _, batch := range batches {
			inv, err := billBatch(ctx, tx, orgID, in, settings.GSTRegistered, batch)
			if err != nil {
				return err
			}
			result.Invoices = append(result.Invoices, RunInvoice{Invoice: inv})
			for _, g := range batch {
				result.EntryCount += len(g.entryIDs)
				result.TotalMinutes += g.totalMinutes
			}
		}
		return nil
	})
	if err != nil {
		return RunResult{}, err
	}

	if in.Issue {
		for i := range result.Invoices {
			inv, err := issueInvoice(b, ctx, orgID, orgSlug, result.Invoices[i].Invoice.ID)
			if err != nil {
				result.Invoices[i].IssueErr = err
				continue
			}
			result.Invoices[i].Invoice = inv
		}
	}
	return result, nil
}

// billBatch creates one draft invoice for groups (already sorted), stamps
// their entries with its line ids and audits it, all inside tx.
func billBatch(ctx context.Context, tx pgx.Tx, orgID int64, in RunInput, gstRegistered bool, groups []taskGroup) (core.Invoice, error) {
	lines := make([]core.NewLine, len(groups))
	var total core.Cents
	entryIDs := []int64{}
	for i, g := range groups {
		lines[i] = LineFor(g.project, g.task, g.totalMinutes, in.From, in.To, gstRegistered)
		total += lines[i].UnitAmount // QuantityMilli is always 1000, so LineAmount == UnitAmount.
		entryIDs = append(entryIDs, g.entryIDs...)
	}
	if total == 0 {
		return core.Invoice{}, fmt.Errorf("%w: billing run total is zero", core.ErrInvalidInput)
	}

	inv, err := core.CreateDraftInvoice(ctx, tx, orgID, core.NewInvoice{
		ContactID: in.ClientContactID,
		Source:    "timesheet",
		DueInDays: in.DueInDays,
		Lines:     lines,
	})
	if err != nil {
		return core.Invoice{}, err
	}
	if len(inv.Lines) != len(groups) {
		return core.Invoice{}, fmt.Errorf("timesheets: created invoice has %d lines, want %d", len(inv.Lines), len(groups))
	}

	for i, g := range groups {
		if _, err := tx.Exec(ctx,
			`UPDATE time_entries SET invoice_line_id = $1 WHERE org_id = $2 AND id = ANY($3)`,
			inv.Lines[i].ID, orgID, g.entryIDs,
		); err != nil {
			return core.Invoice{}, err
		}
	}

	if err := core.Audit(ctx, tx, orgID, billing.ActorFrom(ctx), "invoice", inv.ID, "billing_run", map[string]any{
		"client_contact_id": in.ClientContactID, "from": in.From.Format(dateLayout), "to": in.To.Format(dateLayout),
		"split": in.Split, "entry_ids": entryIDs,
	}); err != nil {
		return core.Invoice{}, err
	}
	return inv, nil
}

// unbilledGroups selects orgID's unbilled time entries for clientID's
// time-and-materials projects (fixed-price time is never billed here)
// within [from, to], restricted to taskIDs unless it is nil, and groups
// them by task. With lock set the entries are locked FOR UPDATE: the
// locked query touches time_entries rows only; the task/project filter is
// a subquery, never a join on the locked rows, so Postgres only locks
// what it's actually billing, and re-checks invoice_line_id IS NULL for
// any row it had to wait on.
func unbilledGroups(ctx context.Context, q core.Querier, orgID, clientID int64, from, to time.Time, taskIDs []int64, lock bool) ([]taskGroup, error) {
	sql := `
		SELECT e.id, e.task_id, e.minutes FROM time_entries e
		WHERE e.org_id=$1 AND e.invoice_line_id IS NULL AND e.work_date BETWEEN $2 AND $3
		AND e.task_id IN (
			SELECT t.id FROM project_tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE t.org_id=$1 AND p.org_id=$1 AND p.client_contact_id=$4
			AND p.billing_method = 'tm'
		)
		AND ($5::bigint[] IS NULL OR e.task_id = ANY($5))
		ORDER BY e.task_id, e.id`
	if lock {
		sql += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, sql, orgID, from, to, clientID, taskIDs)
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
	rows.Close()
	if len(order) == 0 {
		return nil, nil
	}

	projects := map[int64]Project{}
	groups := make([]taskGroup, 0, len(order))
	for _, taskID := range order {
		g := byTask[taskID]
		task, err := GetTask(ctx, q, orgID, taskID)
		if err != nil {
			return nil, err
		}
		project, ok := projects[task.ProjectID]
		if !ok {
			project, err = GetProject(ctx, q, orgID, task.ProjectID)
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
