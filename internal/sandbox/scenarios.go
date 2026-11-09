package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
)

func seedScenarios(ctx context.Context, tx pgx.Tx, today time.Time, orgs []core.Org, users map[string]int64) error {
	contacts := make(map[string]int64)
	for _, def := range accountDefinitions {
		if def.role != "tenant" && def.role != "client" {
			continue
		}
		var id int64
		if err := tx.QueryRow(ctx, `SELECT contact_id FROM memberships WHERE user_id=$1 AND org_id=$2`,
			users[def.name], orgs[def.org].ID).Scan(&id); err != nil {
			return err
		}
		contacts[def.name] = id
	}
	for i, org := range orgs {
		if _, err := core.UpdateSettings(ctx, tx, org.ID, core.OrgSettings{
			Name: org.Name, GSTRegistered: i == 0, BASFrequency: "quarterly", InvoiceLeadDays: 7,
			BankBSB: "000-000", BankAccount: "00000000", PayID: "sandbox@example.test",
		}); err != nil {
			return err
		}
		tenant, client, actor := contacts["tenant"], contacts["client"], users["staff"]
		if i == 1 {
			tenant, client, actor = contacts["tenant-beta"], contacts["client-beta"], users["owner"]
		}
		properties, err := seedProperties(ctx, tx, org.ID, today, tenant, contacts["tenant-other"], i == 0)
		if err != nil {
			return fmt.Errorf("property scenarios: %w", err)
		}
		projectID, err := seedTimesheets(ctx, tx, org.ID, today, client, actor, i == 0)
		if err != nil {
			return fmt.Errorf("timesheet scenarios: %w", err)
		}
		n := 12
		if i == 0 {
			n = 60
		}
		states := []string{"draft", "upcoming", "overdue", "partial", "paid", "void"}
		for j := range n {
			contact := client
			if j%3 == 0 {
				contact = tenant
			} else if j%3 == 1 && i == 0 {
				contact = contacts["tenant-other"]
			}
			if _, err := seedInvoice(ctx, tx, org.ID, core.NewInvoice{
				ContactID: contact, Source: "manual", DueInDays: 14,
				Lines: []core.NewLine{{Description: fmt.Sprintf("Sandbox services %02d — %s", j+1, states[j%6]),
					QuantityMilli: 1000, UnitAmount: core.Cents(7500 + j*375), GSTCode: "FRE"}},
			}, today, states[j%6]); err != nil {
				return err
			}
		}
		if err := seedReconciliation(ctx, tx, org.ID, today, properties, projectID); err != nil {
			return fmt.Errorf("reconciliation scenarios: %w", err)
		}
	}
	return nil
}

func seedInvoice(ctx context.Context, tx pgx.Tx, orgID int64, in core.NewInvoice, today time.Time, state string) (core.Invoice, error) {
	inv, err := core.CreateDraftInvoice(ctx, tx, orgID, in)
	if err != nil || state == "draft" {
		return inv, err
	}
	issued := today.AddDate(0, 0, -35)
	if state == "upcoming" {
		issued = today.AddDate(0, 0, -1)
	}
	if in.DueOn != nil {
		issued = *in.DueOn
	}
	inv, err = core.IssueInvoice(ctx, tx, orgID, inv.ID, issued)
	if err != nil {
		return core.Invoice{}, err
	}
	switch state {
	case "partial", "paid":
		amount := inv.Total
		if state == "partial" {
			amount /= 2
		}
		paidOn := today.AddDate(0, 0, -3)
		if paidOn.Before(issued) {
			paidOn = issued
		}
		if _, _, err := core.RecordPayment(ctx, tx, orgID, core.NewPayment{
			InvoiceID: inv.ID, Amount: amount, PaidOn: paidOn, Method: "bank_transfer",
		}); err != nil {
			return core.Invoice{}, err
		}
	case "void":
		return core.VoidInvoice(ctx, tx, orgID, inv.ID)
	}
	return core.GetInvoice(ctx, tx, orgID, inv.ID)
}
