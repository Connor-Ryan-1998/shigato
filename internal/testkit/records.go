//go:build integration

package testkit

import (
	"time"

	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/payments"
	"shigoto/internal/property"
	"shigoto/internal/timesheets"
)

// Date parses a 2006-01-02 literal as UTC midnight. It panics on a bad
// literal, which is always a typo in the test.
func Date(s string) time.Time {
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		panic("testkit.Date: " + err.Error())
	}
	return d
}

// --- contacts ---

// ContactOption customises Env.Contact.
type ContactOption func(*core.Contact)

// Kind sets the contact kind (client, tenant or supplier).
func Kind(k string) ContactOption { return func(c *core.Contact) { c.Kind = k } }

// Name sets the contact name.
func Name(n string) ContactOption { return func(c *core.Contact) { c.Name = n } }

// Email sets the contact email.
func Email(e string) ContactOption { return func(c *core.Contact) { c.Email = e } }

// Contact creates a contact in org. Defaults: a client named "Test Contact".
func (e *Env) Contact(org core.Org, opts ...ContactOption) core.Contact {
	e.T.Helper()
	in := core.Contact{Kind: "client", Name: "Test Contact"}
	for _, opt := range opts {
		opt(&in)
	}
	c, err := core.CreateContact(e.Ctx, e.Pool, org.ID, in)
	if err != nil {
		e.T.Fatalf("creating %s contact %q in %s: %v", in.Kind, in.Name, org.Slug, err)
		return core.Contact{}
	}
	return c
}

// --- invoices ---

type invoiceConfig struct {
	amount     core.Cents
	gstCode    string
	propertyID *int64
	source     string
	dueIn      int
	issue      *time.Time
}

// InvoiceOption customises Env.Invoice.
type InvoiceOption func(*invoiceConfig)

// Amount sets the unit amount of the invoice's single line (its total
// when the line is GST-free).
func Amount(cents int64) InvoiceOption {
	return func(c *invoiceConfig) { c.amount = core.Cents(cents) }
}

// InvoiceGST sets the GST code of the invoice's single line (GST, FRE,
// INP, N-T...). A GST-coded line needs a GST-registered org.
func InvoiceGST(code string) InvoiceOption {
	return func(c *invoiceConfig) { c.gstCode = code }
}

// LineProperty tags the invoice's single line to propertyID.
func LineProperty(propertyID int64) InvoiceOption {
	return func(c *invoiceConfig) { c.propertyID = &propertyID }
}

// DueIn sets the invoice's payment term: due days after its issue date.
func DueIn(days int) InvoiceOption { return func(c *invoiceConfig) { c.dueIn = days } }

// Source sets the invoice source (manual, rent, timesheet...).
func Source(s string) InvoiceOption { return func(c *invoiceConfig) { c.source = s } }

// Issued issues the invoice dated today, in its own transaction.
func Issued(today time.Time) InvoiceOption {
	return func(c *invoiceConfig) { c.issue = &today }
}

// Invoice creates an invoice to contactID with one line. Defaults: a
// 1000-cent N-T (GST-free) manual draft due 14 days after issue, untagged.
func (e *Env) Invoice(org core.Org, contactID int64, opts ...InvoiceOption) core.Invoice {
	e.T.Helper()
	c := invoiceConfig{amount: 1000, gstCode: "N-T", source: "manual", dueIn: 14}
	for _, opt := range opts {
		opt(&c)
	}
	inv, err := core.CreateDraftInvoice(e.Ctx, e.Pool, org.ID, core.NewInvoice{
		ContactID: contactID,
		Source:    c.source,
		DueInDays: c.dueIn,
		Lines: []core.NewLine{
			{Description: "line", QuantityMilli: 1000, UnitAmount: c.amount, GSTCode: c.gstCode, PropertyID: c.propertyID},
		},
	})
	if err != nil {
		e.T.Fatalf("creating %d-cent %s %s draft invoice in %s: %v", c.amount, c.gstCode, c.source, org.Slug, err)
		return core.Invoice{}
	}
	if c.issue == nil {
		return inv
	}
	return e.Issue(org, inv.ID, *c.issue)
}

// Issue issues the existing draft invoiceID dated today, in its own
// transaction, through core.IssueInvoice (no pay-options check, no send).
func (e *Env) Issue(org core.Org, invoiceID int64, today time.Time) core.Invoice {
	e.T.Helper()
	tx, err := e.Pool.Begin(e.Ctx)
	if err != nil {
		e.T.Fatalf("beginning issue of invoice %d: %v", invoiceID, err)
		return core.Invoice{}
	}
	defer func() { _ = tx.Rollback(e.Ctx) }()
	inv, err := core.IssueInvoice(e.Ctx, tx, org.ID, invoiceID, today)
	if err != nil {
		e.T.Fatalf("issuing invoice %d in %s: %v", invoiceID, org.Slug, err)
		return core.Invoice{}
	}
	if err := tx.Commit(e.Ctx); err != nil {
		e.T.Fatalf("committing issue of invoice %d: %v", invoiceID, err)
		return core.Invoice{}
	}
	return inv
}

// --- properties and leases ---

// PropertyOption customises Env.Property.
type PropertyOption func(*property.Property)

// Commercial makes the property non-residential.
func Commercial() PropertyOption {
	return func(p *property.Property) { p.Residential = false }
}

// Residential makes the property residential (the default), for tests
// whose assertions depend on residency.
func Residential() PropertyOption {
	return func(p *property.Property) { p.Residential = true }
}

// PropertyName sets the property name.
func PropertyName(n string) PropertyOption {
	return func(p *property.Property) { p.Name = n }
}

// Property creates a property in org. Defaults: residential "1 Test St" at
// "1 Test St, Testville".
func (e *Env) Property(org core.Org, opts ...PropertyOption) property.Property {
	e.T.Helper()
	in := property.Property{Name: "1 Test St", Address: "1 Test St, Testville", Residential: true}
	for _, opt := range opts {
		opt(&in)
	}
	p, err := property.CreateProperty(e.Ctx, e.Pool, org.ID, in)
	if err != nil {
		e.T.Fatalf("creating property %q in %s: %v", in.Name, org.Slug, err)
		return property.Property{}
	}
	return p
}

// LeaseOption customises Env.Lease.
type LeaseOption func(*property.NewLease)

// Start sets the lease start date.
func Start(d time.Time) LeaseOption { return func(l *property.NewLease) { l.StartDate = d } }

// Frequency sets how often rent is charged.
func Frequency(f property.Frequency) LeaseOption {
	return func(l *property.NewLease) { l.Frequency = f }
}

// Rent sets the initial rent amount per period.
func Rent(c core.Cents) LeaseOption { return func(l *property.NewLease) { l.RentAmount = c } }

// CoTenant adds contactID as a non-primary tenant.
func CoTenant(contactID int64) LeaseOption {
	return func(l *property.NewLease) {
		l.Tenants = append(l.Tenants, property.NewLeaseTenant{ContactID: contactID})
	}
}

// PaidTo sets the date rent is already paid to (the last day of a period).
func PaidTo(d time.Time) LeaseOption { return func(l *property.NewLease) { l.PaidToDate = &d } }

// Arrears sets the opening arrears, which raises a draft arrears invoice.
func Arrears(c core.Cents) LeaseOption {
	return func(l *property.NewLease) { l.OpeningArrears = c }
}

// Bond sets the bond amount.
func Bond(c core.Cents) LeaseOption { return func(l *property.NewLease) { l.BondAmount = c } }

// Lease creates a lease on propertyID with tenantContactID as its primary
// tenant. Defaults: weekly from 2026-01-05 at 50000 cents, no other tenant.
func (e *Env) Lease(org core.Org, propertyID, tenantContactID int64, opts ...LeaseOption) property.Lease {
	e.T.Helper()
	in := property.NewLease{
		PropertyID: propertyID,
		StartDate:  Date("2026-01-05"),
		Frequency:  property.Weekly,
		RentAmount: 50000,
		Tenants:    []property.NewLeaseTenant{{ContactID: tenantContactID, Primary: true}},
	}
	for _, opt := range opts {
		opt(&in)
	}
	l, err := property.CreateLease(e.Ctx, e.Pool, org.ID, in)
	if err != nil {
		e.T.Fatalf("creating lease on property %d for tenant %d in %s: %v", propertyID, tenantContactID, org.Slug, err)
		return property.Lease{}
	}
	return l
}

// --- timesheets ---

// ProjectOption customises Env.Project.
type ProjectOption func(*timesheets.Project)

// ProjectName sets the project name.
func ProjectName(n string) ProjectOption { return func(p *timesheets.Project) { p.Name = n } }

// ProjectRate sets the project's hourly rate.
func ProjectRate(c core.Cents) ProjectOption {
	return func(p *timesheets.Project) { p.HourlyRate = c }
}

// ProjectGST sets the project's GST code (GST, FRE, N-T...).
func ProjectGST(code string) ProjectOption {
	return func(p *timesheets.Project) { p.GSTCode = core.GSTCode(code) }
}

// ProjectStatus sets the project status (pending, in_progress, ...).
func ProjectStatus(s string) ProjectOption { return func(p *timesheets.Project) { p.Status = s } }

// FixedPrice makes the project fixed-price, so its time is never billed.
func FixedPrice() ProjectOption {
	return func(p *timesheets.Project) { p.BillingMethod = timesheets.BillingFixed }
}

// Project creates a project for clientID. Defaults: "Website rebuild" at
// 12000 cents an hour, GST, in progress, time and materials.
func (e *Env) Project(org core.Org, clientID int64, opts ...ProjectOption) timesheets.Project {
	e.T.Helper()
	in := timesheets.Project{ClientContactID: clientID, Name: "Website rebuild", HourlyRate: 12000, GSTCode: core.GST}
	for _, opt := range opts {
		opt(&in)
	}
	p, err := timesheets.CreateProject(e.Ctx, e.Pool, org.ID, in)
	if err != nil {
		e.T.Fatalf("creating project %q in %s: %v", in.Name, org.Slug, err)
		return timesheets.Project{}
	}
	return p
}

// TaskOption customises Env.Task.
type TaskOption func(*timesheets.Task)

// TaskName sets the task name.
func TaskName(n string) TaskOption { return func(t *timesheets.Task) { t.Name = n } }

// TaskRate overrides the project's hourly rate for this task.
func TaskRate(c core.Cents) TaskOption { return func(t *timesheets.Task) { t.HourlyRate = &c } }

// Task creates a task in projectID. Defaults: named "Development", using
// the project's rate.
func (e *Env) Task(org core.Org, projectID int64, opts ...TaskOption) timesheets.Task {
	e.T.Helper()
	in := timesheets.Task{Name: "Development"}
	for _, opt := range opts {
		opt(&in)
	}
	t, err := timesheets.CreateTask(e.Ctx, e.Pool, org.ID, projectID, in)
	if err != nil {
		e.T.Fatalf("creating task %q in project %d of %s: %v", in.Name, projectID, org.Slug, err)
		return timesheets.Task{}
	}
	return t
}

// Entry logs entry under userID, with today as the caller's current date.
func (e *Env) Entry(org core.Org, userID int64, entry timesheets.Entry, today time.Time) timesheets.Entry {
	e.T.Helper()
	got, err := timesheets.CreateEntry(e.Ctx, e.Pool, org.ID, userID, entry, today)
	if err != nil {
		e.T.Fatalf("logging %d minutes on task %d for user %d in %s: %v", entry.Minutes, entry.TaskID, userID, org.Slug, err)
		return timesheets.Entry{}
	}
	return got
}

// --- org settings ---

type settingsConfig struct {
	gst        *bool
	payOptions bool
	rentRun    *rentRunConfig
}

type rentRunConfig struct {
	leadDays int
	autoSend bool
}

// SettingsOption customises Env.Settings.
type SettingsOption func(*settingsConfig)

// GSTRegistered sets the org's gst_registered flag.
func GSTRegistered(registered bool) SettingsOption {
	return func(c *settingsConfig) { c.gst = &registered }
}

// PayOptions gives the org bank transfer (BSB 062-000, account 12345678)
// and PayID (owner@example.com), satisfying billing.Service.Issue's
// pay-options rule.
func PayOptions() SettingsOption { return func(c *settingsConfig) { c.payOptions = true } }

// RentRun sets the org's invoice_lead_days and auto_send_rent, which
// control how far ahead the rent run invoices and whether it issues them.
func RentRun(leadDays int, autoSend bool) SettingsOption {
	return func(c *settingsConfig) { c.rentRun = &rentRunConfig{leadDays: leadDays, autoSend: autoSend} }
}

// Settings updates org settings directly, bypassing OrgSettings validation.
func (e *Env) Settings(org core.Org, opts ...SettingsOption) {
	e.T.Helper()
	var c settingsConfig
	for _, opt := range opts {
		opt(&c)
	}
	if c.gst != nil {
		if _, err := e.Pool.Exec(e.Ctx, "UPDATE orgs SET gst_registered = $1 WHERE id = $2", *c.gst, org.ID); err != nil {
			e.T.Fatalf("setting gst_registered of %s: %v", org.Slug, err)
			return
		}
	}
	if c.payOptions {
		if _, err := e.Pool.Exec(e.Ctx,
			"UPDATE orgs SET bank_bsb = $1, bank_account = $2, payid = $3 WHERE id = $4",
			"062-000", "12345678", "owner@example.com", org.ID,
		); err != nil {
			e.T.Fatalf("setting pay options of %s: %v", org.Slug, err)
			return
		}
	}
	if c.rentRun != nil {
		if _, err := e.Pool.Exec(e.Ctx,
			"UPDATE orgs SET invoice_lead_days = $1, auto_send_rent = $2 WHERE id = $3",
			c.rentRun.leadDays, c.rentRun.autoSend, org.ID,
		); err != nil {
			e.T.Fatalf("setting rent run settings of %s: %v", org.Slug, err)
			return
		}
	}
}

// --- billing ---

// BillingOption customises Env.BillingService.
type BillingOption func(*billing.Service)

// Provider makes every org resolve to p.
func Provider(p payments.Provider) BillingOption {
	return func(s *billing.Service) {
		s.Payments = func(string) (payments.Provider, error) { return p, nil }
	}
}

// BillingService returns a UTC billing.Service over e's database whose
// orgs have no payment provider (payments.ErrNoProvider) and whose GetEnv
// returns "".
func (e *Env) BillingService(opts ...BillingOption) *billing.Service {
	s := &billing.Service{
		Pool:     e.Pool,
		GetEnv:   func(string) string { return "" },
		Payments: func(string) (payments.Provider, error) { return nil, payments.ErrNoProvider },
		Location: time.UTC,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}
