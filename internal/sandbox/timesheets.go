package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
	"shigoto/internal/timesheets"
)

func seedTimesheets(ctx context.Context, tx pgx.Tx, orgID int64, today time.Time, client, actor int64, registered bool) (int64, error) {
	var firstProject int64
	for i, status := range []string{timesheets.StatusInProgress, timesheets.StatusPending, timesheets.StatusCompleted, timesheets.StatusCancelled} {
		start := today.AddDate(0, 0, -60)
		project, err := timesheets.CreateProject(ctx, tx, orgID, timesheets.Project{
			Name:            []string{"Website refresh", "Reporting setup", "Systems review", "Archived proposal"}[i],
			ClientContactID: client, HourlyRate: 12000, GSTCode: core.GST, StartDate: &start,
			Status: status, BillingMethod: timesheets.BillingTM, Description: "Fictional engagement for local workflow testing.",
		})
		if err != nil {
			return 0, err
		}
		if i == 0 {
			firstProject = project.ID
		}
		for _, name := range []string{"Discovery", "Delivery"} {
			task, err := timesheets.CreateTask(ctx, tx, orgID, project.ID, timesheets.Task{Name: name})
			if err != nil {
				return 0, err
			}
			if i != 0 {
				continue
			}
			var billed []int64
			for j := range 6 {
				entry, err := timesheets.CreateEntry(ctx, tx, orgID, actor, timesheets.Entry{
					TaskID: task.ID, WorkDate: today.AddDate(0, 0, -18+j*2), Minutes: 60 + j*30,
					Note: fmt.Sprintf("%s session %d", name, j+1),
				}, today)
				if err != nil {
					return 0, err
				}
				if j < 3 {
					billed = append(billed, entry.ID)
				}
			}
			line := timesheets.LineFor(project, task, 270, today.AddDate(0, 0, -18), today.AddDate(0, 0, -14), registered)
			inv, err := seedInvoice(ctx, tx, orgID, core.NewInvoice{
				ContactID: client, Source: "timesheet", DueInDays: 14, Lines: []core.NewLine{line},
			}, today, "upcoming")
			if err != nil {
				return 0, err
			}
			if _, err := tx.Exec(ctx, `UPDATE time_entries SET invoice_line_id=$1 WHERE org_id=$2 AND id=ANY($3)`, inv.Lines[0].ID, orgID, billed); err != nil {
				return 0, err
			}
		}
	}
	return firstProject, nil
}
