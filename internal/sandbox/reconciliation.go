package sandbox

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
	"shigoto/internal/importers"
	"shigoto/internal/property"
	"shigoto/internal/recon"
)

func seedReconciliation(ctx context.Context, tx pgx.Tx, orgID int64, today time.Time, properties []property.Property, projectID int64) error {
	supplier, err := core.CreateContact(ctx, tx, orgID, core.Contact{Kind: "supplier", Name: "Sandbox Hardware & Services", Email: "supplier@example.test"})
	if err != nil {
		return err
	}
	var repairs, software int64
	if err := tx.QueryRow(ctx, `SELECT id FROM categories WHERE org_id=$1 AND name='Repairs & maintenance'`, orgID).Scan(&repairs); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM categories WHERE org_id=$1 AND name='Software & subscriptions'`, orgID).Scan(&software); err != nil {
		return err
	}
	var matchedExpense int64
	for i := range 12 {
		p := properties[i%len(properties)]
		lines := []recon.NewExpenseLine{{CategoryID: repairs, Amount: 11000, PropertyID: &p.ID},
			{CategoryID: software, Amount: 5500, ProjectID: &projectID}}
		expense, _, err := recon.CreateExpense(ctx, tx, orgID, recon.NewExpense{
			SpentOn: today.AddDate(0, 0, -i*3), SupplierContactID: &supplier.ID,
			Description: fmt.Sprintf("Maintenance and software — batch %02d", i+1), Lines: lines,
		})
		if err != nil {
			return err
		}
		if i == 0 {
			matchedExpense = expense.ID
		}
	}
	amountColumn := 1
	mapping := importers.CSVMapping{HasHeader: true, DateColumn: 0, AmountColumn: &amountColumn, DescriptionColumn: 2, DateFormat: "2006-01-02"}
	account, err := recon.CreateBankAccount(ctx, tx, orgID, "Sandbox operating account", mapping)
	if err != nil {
		return err
	}
	var invoiceID, amount int64
	var reference string
	if err := tx.QueryRow(ctx, `SELECT id,total,payment_reference FROM invoices i WHERE org_id=$1 AND status='open'
		AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.invoice_id=i.id) ORDER BY id LIMIT 1`, orgID).Scan(&invoiceID, &amount, &reference); err != nil {
		return err
	}
	date := today.Format("2006-01-02")
	csv := fmt.Sprintf("Date,Amount,Description\n%s,%d.%02d,Payment %s\n%s,-165.00,Maintenance and software\n%s,100.00,Unallocated tenant deposit\n%s,-42.50,Unmatched hardware purchase\n%s,-12.00,Personal transaction\n%s,250.00,Client transfer pending allocation\n",
		date, amount/100, amount%100, reference, date, date, date, date, date)
	lines, err := importers.NewCSV(mapping).Lines(strings.NewReader(csv))
	if err != nil {
		return err
	}
	keys := importers.DedupeKeys(lines)
	sum := sha256.Sum256([]byte(csv))
	var importID int64
	// Import owns a separate transaction; fixture inserts stay inside the atomic reset.
	if err := tx.QueryRow(ctx, `INSERT INTO bank_imports (org_id,bank_account_id,file_sha256,rows_total,rows_new) VALUES ($1,$2,$3,$4,$4) RETURNING id`,
		orgID, account.ID, sum[:], len(lines)).Scan(&importID); err != nil {
		return err
	}
	for i, line := range lines {
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO bank_lines (org_id,bank_account_id,import_id,posted_on,amount,description,dedupe_key) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			orgID, account.ID, importID, line.PostedOn, int64(line.Amount), line.Description, keys[i]).Scan(&id); err != nil {
			return err
		}
		switch i {
		case 0:
			if _, err := recon.MatchInvoice(ctx, tx, orgID, id, invoiceID, "bank_transfer"); err != nil {
				return err
			}
		case 1:
			if err := recon.MatchExpense(ctx, tx, orgID, id, matchedExpense); err != nil {
				return err
			}
		case 4:
			if err := recon.Ignore(ctx, tx, orgID, id, "drawings"); err != nil {
				return err
			}
		}
	}
	return nil
}
