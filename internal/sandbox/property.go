package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
	"shigoto/internal/property"
)

func seedProperties(ctx context.Context, tx pgx.Tx, orgID int64, today time.Time, tenant, otherTenant int64, main bool) ([]property.Property, error) {
	definitions := []struct {
		name, address string
		residential   bool
		frequency     property.Frequency
		rent          core.Cents
		ended         bool
	}{
		{"River Cottage", "12 Example Street, Brisbane QLD", true, property.Weekly, 52000, false},
		{"Garden Terrace", "24 Sample Avenue, Brisbane QLD", true, property.Fortnightly, 98000, false},
		{"Market Studio", "3 Demo Lane, Brisbane QLD", false, property.Monthly, 180000, false},
		{"Previous Home", "8 Fiction Road, Brisbane QLD", true, property.Weekly, 45000, true},
	}
	if !main {
		definitions = definitions[:1]
	}
	var properties []property.Property
	for i, def := range definitions {
		p, err := property.CreateProperty(ctx, tx, orgID, property.Property{
			Name: def.name, Address: def.address, Kind: "house", Residential: def.residential,
		})
		if err != nil {
			return nil, err
		}
		properties = append(properties, p)
		contact := tenant
		if main && (i == 1 || i == 2) {
			contact = otherTenant
		}
		start := today.AddDate(0, 0, -84)
		end := today.AddDate(1, 0, 0)
		if def.ended {
			start, end = today.AddDate(0, 0, -168), today.AddDate(0, 0, -91)
		}
		lease, err := property.CreateLease(ctx, tx, orgID, property.NewLease{
			PropertyID: p.ID, Tenants: []property.NewLeaseTenant{{ContactID: contact, Primary: true}},
			StartDate: start, EndDate: &end, Frequency: def.frequency, RentAmount: def.rent, BondAmount: 208000,
		})
		if err != nil {
			return nil, err
		}
		if def.ended {
			lease, err = property.TerminateLease(ctx, tx, orgID, lease.ID, end)
			if err != nil {
				return nil, err
			}
		} else {
			if _, _, err := property.CreateRentChange(ctx, tx, orgID, lease.ID, today.AddDate(0, 0, -28), def.rent+2500); err != nil {
				return nil, err
			}
			if _, err := property.CreateCharge(ctx, tx, orgID, lease.ID, property.NewCharge{
				Description: "Water usage", Amount: 1500, StartDate: start,
			}); err != nil {
				return nil, err
			}
			lease, err = property.GetLease(ctx, tx, orgID, lease.ID)
			if err != nil {
				return nil, err
			}
		}
		code := "INP"
		if !def.residential {
			code = "GST"
		}
		periods := property.PeriodsToInvoice(lease.ToTerms(), today, 7)
		states := []string{"paid", "paid", "partial", "overdue", "draft"}
		for j, period := range periods {
			lines := []core.NewLine{{Description: fmt.Sprintf("Rent %s–%s", period.Start.Format("2006-01-02"), period.End.Format("2006-01-02")),
				QuantityMilli: 1000, UnitAmount: period.Rent, GSTCode: code, PropertyID: &p.ID}}
			for _, charge := range period.Charges {
				lines = append(lines, core.NewLine{Description: charge.Description, QuantityMilli: 1000, UnitAmount: charge.Amount, GSTCode: code, PropertyID: &p.ID})
			}
			state := states[j%len(states)]
			if period.Start.After(today) {
				state = "draft"
			}
			inv, err := seedInvoice(ctx, tx, orgID, core.NewInvoice{
				ContactID: contact, Source: "rent", DueOn: &period.Start, Lines: lines,
			}, today, state)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO rent_periods (org_id,lease_id,period_start,period_end,amount_due,invoice_id) VALUES ($1,$2,$3,$4,$5,$6)`,
				orgID, lease.ID, period.Start, period.End, int64(inv.Total), inv.ID); err != nil {
				return nil, err
			}
		}
		bond, err := seedInvoice(ctx, tx, orgID, core.NewInvoice{
			ContactID: contact, Source: "bond", DueInDays: 14,
			Lines: []core.NewLine{{Description: "Rental bond — " + p.Name, QuantityMilli: 1000, UnitAmount: lease.BondAmount, GSTCode: "N-T", PropertyID: &p.ID}},
		}, today, "paid")
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE leases SET bond_invoice_id=$1 WHERE id=$2`, bond.ID, lease.ID); err != nil {
			return nil, err
		}
	}
	if _, err := property.CreateProperty(ctx, tx, orgID, property.Property{Name: "Vacant Apartment", Address: "6 Test Court, Brisbane QLD", Kind: "unit", Residential: true}); err != nil {
		return nil, err
	}
	return properties, nil
}
