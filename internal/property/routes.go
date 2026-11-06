package property

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

// dateLayout matches httpapi's own date encoding (YYYY-MM-DD).
const dateLayout = "2006-01-02"

// Module returns the property module: properties, leases, rent changes,
// charges and lease termination, gated by config.Config.Modules["property"].
func Module() module.Module {
	return module.Module{Name: "property", Routes: registerRoutes, Dashboard: dashboardSection}
}

// propertyDashboard is the property module's dashboard section.
type propertyDashboard struct {
	ArrearsTotal         int64 `json:"arrears_total"`
	UnissuedTotal        int64 `json:"unissued_total"`
	BondsUnlodgedOverdue int   `json:"bonds_unlodged_overdue"`
}

// dashboardSection builds the property module's dashboard section: arrears
// and unissued totals, and the count of leases whose bond is held but not
// lodged with the RTA within the warning window, summed across every
// lease in the org. It reuses GetLedger and BondInfo - the same maths the
// ledger and lease endpoints already use - rather than recomputing
// arrears/unissued/bond-warning logic here.
//
// ponytail: this is one GetLedger plus one GetLease/BondInfo call per
// lease (GetLedger itself also loads the lease), not a single aggregate
// query - reusing the existing per-lease maths outweighs duplicating it in
// SQL. Fine at today's lease counts per org; replace with an aggregate
// query (or a batched GetLedger) if an org's lease count ever makes this
// dashboard call slow.
func dashboardSection(ctx context.Context, q core.Querier, orgID int64, today time.Time) (any, error) {
	leaseIDs, err := listLeaseIDs(ctx, q, orgID)
	if err != nil {
		return nil, err
	}

	var arrears, unissued core.Cents
	var bondsUnlodgedOverdue int
	for _, id := range leaseIDs {
		ledger, err := GetLedger(ctx, q, orgID, id, today)
		if err != nil {
			return nil, err
		}
		arrears += ledger.Arrears
		unissued += ledger.Unissued

		lease, err := GetLease(ctx, q, orgID, id)
		if err != nil {
			return nil, err
		}
		status, warnings, err := BondInfo(ctx, q, orgID, lease, today)
		if err != nil {
			return nil, err
		}
		if status == "held" && len(warnings) > 0 {
			bondsUnlodgedOverdue++
		}
	}

	return propertyDashboard{
		ArrearsTotal:         int64(arrears),
		UnissuedTotal:        int64(unissued),
		BondsUnlodgedOverdue: bondsUnlodgedOverdue,
	}, nil
}

func registerRoutes(mux *http.ServeMux, d module.Deps) {
	mux.Handle("POST /api/v1/orgs/{org}/properties",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createProperty(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/properties",
		d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listProperties(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/properties/{id}",
		d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getProperty(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/leases",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createLease(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/leases/{id}",
		d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getLease(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/leases",
		d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listLeases(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/leases/{id}/rent-changes",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createRentChange(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/leases/{id}/charges",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createCharge(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/leases/{id}/terminate",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			terminateLease(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/leases/{id}/ledger",
		d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getLedger(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/rent-run",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			runRent(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/leases/{id}/bond-invoice",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createBondInvoiceHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/leases/{id}/bond-lodgement",
		d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createBondLodgementHandler(w, r, d)
		})))
}

// --- request/response shapes ---

type propertyRequest struct {
	Name        string `json:"name"`
	Address     string `json:"address,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Residential *bool  `json:"residential,omitempty"`
}

type propertyResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Residential bool   `json:"residential"`
}

func propertyToResponse(p Property) propertyResponse {
	return propertyResponse{ID: p.ID, Name: p.Name, Address: p.Address, Kind: p.Kind, Residential: p.Residential}
}

type leaseTenantRequest struct {
	ContactID int64 `json:"contact_id"`
	Primary   bool  `json:"primary"`
}

type leaseTenantResponse struct {
	ContactID int64 `json:"contact_id"`
	Primary   bool  `json:"primary"`
}

type leaseRequest struct {
	PropertyID     int64                `json:"property_id"`
	Tenants        []leaseTenantRequest `json:"tenants"`
	StartDate      string               `json:"start_date"`
	EndDate        string               `json:"end_date,omitempty"`
	Frequency      string               `json:"frequency"`
	RentAmount     int64                `json:"rent_amount"`
	PaidToDate     string               `json:"paid_to_date,omitempty"`
	OpeningArrears int64                `json:"opening_arrears,omitempty"`
	BondAmount     int64                `json:"bond_amount,omitempty"`
}

type rentChangeResponse struct {
	ID            int64  `json:"id"`
	EffectiveDate string `json:"effective_date"`
	Amount        int64  `json:"amount"`
}

func rentChangeToResponse(c LeaseRentChange) rentChangeResponse {
	return rentChangeResponse{ID: c.ID, EffectiveDate: c.EffectiveDate.Format(dateLayout), Amount: int64(c.Amount)}
}

type chargeResponse struct {
	ID          int64   `json:"id"`
	Description string  `json:"description"`
	Amount      int64   `json:"amount"`
	StartDate   string  `json:"start_date"`
	EndDate     *string `json:"end_date,omitempty"`
}

func chargeToResponse(c LeaseCharge) chargeResponse {
	resp := chargeResponse{ID: c.ID, Description: c.Description, Amount: int64(c.Amount), StartDate: c.StartDate.Format(dateLayout)}
	if c.EndDate != nil {
		s := c.EndDate.Format(dateLayout)
		resp.EndDate = &s
	}
	return resp
}

type leaseResponse struct {
	ID                      int64                 `json:"id"`
	PropertyID              int64                 `json:"property_id"`
	StartDate               string                `json:"start_date"`
	EndDate                 *string               `json:"end_date,omitempty"`
	TerminationDate         *string               `json:"termination_date,omitempty"`
	Frequency               string                `json:"frequency"`
	PaidToDate              *string               `json:"paid_to_date,omitempty"`
	OpeningArrears          int64                 `json:"opening_arrears"`
	OpeningArrearsInvoiceID *int64                `json:"opening_arrears_invoice_id,omitempty"`
	BondAmount              int64                 `json:"bond_amount"`
	RTALodgementRef         string                `json:"rta_lodgement_ref,omitempty"`
	BondLodgedOn            *string               `json:"bond_lodged_on,omitempty"`
	BondStatus              string                `json:"bond_status"`
	Warnings                []string              `json:"warnings,omitempty"`
	Tenants                 []leaseTenantResponse `json:"tenants"`
	RentChanges             []rentChangeResponse  `json:"rent_changes"`
	Charges                 []chargeResponse      `json:"charges"`
}

// leaseToResponse builds l's JSON response. bondStatus and warnings come
// from BondInfo, which needs DB access leaseToResponse itself doesn't have;
// callers compute them first and merge them in here, together with
// leaseWarnings as of today, rather than duplicating a warnings field.
func leaseToResponse(l Lease, bondStatus string, warnings []string, today time.Time) leaseResponse {
	resp := leaseResponse{
		ID: l.ID, PropertyID: l.PropertyID, StartDate: l.StartDate.Format(dateLayout),
		Frequency: string(l.Frequency), OpeningArrears: int64(l.OpeningArrears), BondAmount: int64(l.BondAmount),
		OpeningArrearsInvoiceID: l.OpeningArrearsInvoiceID,
		RTALodgementRef:         l.RTALodgementRef,
		BondStatus:              bondStatus,
		Warnings:                append(warnings, leaseWarnings(l, today)...),
		Tenants:                 make([]leaseTenantResponse, len(l.Tenants)),
		RentChanges:             make([]rentChangeResponse, len(l.RentChanges)),
		Charges:                 make([]chargeResponse, len(l.Charges)),
	}
	if l.EndDate != nil {
		s := l.EndDate.Format(dateLayout)
		resp.EndDate = &s
	}
	if l.TerminationDate != nil {
		s := l.TerminationDate.Format(dateLayout)
		resp.TerminationDate = &s
	}
	if l.PaidToDate != nil {
		s := l.PaidToDate.Format(dateLayout)
		resp.PaidToDate = &s
	}
	if l.BondLodgedOn != nil {
		s := l.BondLodgedOn.Format(dateLayout)
		resp.BondLodgedOn = &s
	}
	for i, t := range l.Tenants {
		resp.Tenants[i] = leaseTenantResponse{ContactID: t.ContactID, Primary: t.IsPrimary}
	}
	for i, c := range l.RentChanges {
		resp.RentChanges[i] = rentChangeToResponse(c)
	}
	for i, c := range l.Charges {
		resp.Charges[i] = chargeToResponse(c)
	}
	return resp
}

// parseDate parses an RFC 3339 date (YYYY-MM-DD) as UTC midnight.
func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation(dateLayout, s, time.UTC)
}

// clockNow is time.Now, overridable by tests so they can pin "today" to a
// specific instant (e.g. an early-morning hour in a +10:00 zone) and assert
// on the calendar date today(d) derives from it.
var clockNow = time.Now

// today returns today's calendar date in d.Cfg.Location (defaulting to
// UTC), as a UTC midnight - the same units parseDate produces. Deriving the
// date this way, rather than returning the instant itself, matters: for a
// location ahead of UTC (e.g. Australia/Brisbane, +10:00), the hours
// between UTC midnight and local midnight would otherwise compare as
// "yesterday" against a parsed date, rejecting same-day input (e.g. a bond
// lodgement dated today, submitted before 10am Brisbane time) and shifting
// BondWarnings' 10-day boundary by a day.
func today(d module.Deps) time.Time {
	loc := d.Cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, dd := clockNow().In(loc).Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
}

// --- properties handlers ---

func createProperty(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body propertyRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	residential := true
	if body.Residential != nil {
		residential = *body.Residential
	}

	p := auth.From(r.Context())
	var created Property
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateProperty(r.Context(), tx, p.OrgID, Property{
			Name: body.Name, Address: body.Address, Kind: body.Kind, Residential: residential,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "property", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, propertyToResponse(created))
}

func listProperties(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	properties, err := ListProperties(r.Context(), d.Pool, p.OrgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]propertyResponse, len(properties))
	for i, pr := range properties {
		resp[i] = propertyToResponse(pr)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func getProperty(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	prop, err := GetProperty(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, propertyToResponse(prop))
}

// --- leases handlers ---

func createLease(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body leaseRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	freq, err := ParseFrequency(body.Frequency)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	startDate, err := parseDate(body.StartDate)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "start_date must be an RFC 3339 date (YYYY-MM-DD)")
		return
	}
	var endDate *time.Time
	if body.EndDate != "" {
		t, err := parseDate(body.EndDate)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "end_date must be an RFC 3339 date (YYYY-MM-DD)")
			return
		}
		endDate = &t
	}
	var paidToDate *time.Time
	if body.PaidToDate != "" {
		t, err := parseDate(body.PaidToDate)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "paid_to_date must be an RFC 3339 date (YYYY-MM-DD)")
			return
		}
		paidToDate = &t
	}

	tenants := make([]NewLeaseTenant, len(body.Tenants))
	for i, t := range body.Tenants {
		tenants[i] = NewLeaseTenant{ContactID: t.ContactID, Primary: t.Primary}
	}

	in := NewLease{
		PropertyID: body.PropertyID, Tenants: tenants, StartDate: startDate, EndDate: endDate,
		Frequency: freq, RentAmount: core.Cents(body.RentAmount), PaidToDate: paidToDate,
		OpeningArrears: core.Cents(body.OpeningArrears), BondAmount: core.Cents(body.BondAmount),
	}

	p := auth.From(r.Context())
	var created Lease
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateLease(r.Context(), tx, p.OrgID, in)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "lease", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	status, warnings, err := BondInfo(r.Context(), d.Pool, p.OrgID, created, today(d))
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, leaseToResponse(created, status, warnings, today(d)))
}

func getLease(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	l, err := GetLease(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	status, warnings, err := BondInfo(r.Context(), d.Pool, p.OrgID, l, today(d))
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, leaseToResponse(l, status, warnings, today(d)))
}

func listLeases(w http.ResponseWriter, r *http.Request, d module.Deps) {
	propertyIDStr := r.URL.Query().Get("property_id")
	propertyID, err := strconv.ParseInt(propertyIDStr, 10, 64)
	if propertyIDStr == "" || err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "property_id is required")
		return
	}

	p := auth.From(r.Context())
	leases, err := ListLeasesByProperty(r.Context(), d.Pool, p.OrgID, propertyID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	now := today(d)
	resp := make([]leaseResponse, len(leases))
	for i, l := range leases {
		status, warnings, err := BondInfo(r.Context(), d.Pool, p.OrgID, l, now)
		if err != nil {
			module.WriteCoreError(w, err)
			return
		}
		resp[i] = leaseToResponse(l, status, warnings, now)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

type rentChangeRequest struct {
	EffectiveDate string `json:"effective_date"`
	Amount        int64  `json:"amount"`
}

type rentChangeWithWarningsResponse struct {
	rentChangeResponse
	Warnings []string `json:"warnings,omitempty"`
}

func createRentChange(w http.ResponseWriter, r *http.Request, d module.Deps) {
	leaseID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body rentChangeRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	effective, err := parseDate(body.EffectiveDate)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "effective_date must be an RFC 3339 date (YYYY-MM-DD)")
		return
	}

	p := auth.From(r.Context())
	var created LeaseRentChange
	var warnings []string
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, warnings, err = CreateRentChange(r.Context(), tx, p.OrgID, leaseID, effective, core.Cents(body.Amount))
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "lease_rent_change", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, rentChangeWithWarningsResponse{
		rentChangeResponse: rentChangeToResponse(created),
		Warnings:           warnings,
	})
}

type chargeRequest struct {
	Description string `json:"description"`
	Amount      int64  `json:"amount"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date,omitempty"`
}

func createCharge(w http.ResponseWriter, r *http.Request, d module.Deps) {
	leaseID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body chargeRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	startDate, err := parseDate(body.StartDate)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "start_date must be an RFC 3339 date (YYYY-MM-DD)")
		return
	}
	var endDate *time.Time
	if body.EndDate != "" {
		t, err := parseDate(body.EndDate)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "end_date must be an RFC 3339 date (YYYY-MM-DD)")
			return
		}
		endDate = &t
	}

	p := auth.From(r.Context())
	var created LeaseCharge
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateCharge(r.Context(), tx, p.OrgID, leaseID, NewCharge{
			Description: body.Description, Amount: core.Cents(body.Amount), StartDate: startDate, EndDate: endDate,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "lease_charge", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, chargeToResponse(created))
}

type terminateRequest struct {
	TerminationDate string `json:"termination_date"`
}

func terminateLease(w http.ResponseWriter, r *http.Request, d module.Deps) {
	leaseID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body terminateRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	terminationDate, err := parseDate(body.TerminationDate)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "termination_date must be an RFC 3339 date (YYYY-MM-DD)")
		return
	}

	p := auth.From(r.Context())
	var updated Lease
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = TerminateLease(r.Context(), tx, p.OrgID, leaseID, terminationDate)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "lease", updated.ID, "terminate", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	status, warnings, err := BondInfo(r.Context(), d.Pool, p.OrgID, updated, today(d))
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, leaseToResponse(updated, status, warnings, today(d)))
}

// --- ledger handler ---

type ledgerPeriodResponse struct {
	Start         string `json:"start"`
	End           string `json:"end"`
	AmountDue     int64  `json:"amount_due"`
	InvoiceID     *int64 `json:"invoice_id,omitempty"`
	InvoiceStatus string `json:"invoice_status,omitempty"`
	Paid          int64  `json:"paid"`
}

type ledgerResponse struct {
	Periods        []ledgerPeriodResponse `json:"periods"`
	OpeningArrears int64                  `json:"opening_arrears"`
	Arrears        int64                  `json:"arrears"`
	InvoicedNotDue int64                  `json:"invoiced_not_due"`
	Unissued       int64                  `json:"unissued"`
}

func ledgerToResponse(l Ledger) ledgerResponse {
	resp := ledgerResponse{
		Periods:        make([]ledgerPeriodResponse, len(l.Periods)),
		OpeningArrears: int64(l.OpeningArrears),
		Arrears:        int64(l.Arrears),
		InvoicedNotDue: int64(l.InvoicedNotDue),
		Unissued:       int64(l.Unissued),
	}
	for i, p := range l.Periods {
		resp.Periods[i] = ledgerPeriodResponse{
			Start: p.Start.Format(dateLayout), End: p.End.Format(dateLayout),
			AmountDue: int64(p.AmountDue), InvoiceID: p.InvoiceID,
			InvoiceStatus: p.InvoiceStatus, Paid: int64(p.Paid),
		}
	}
	return resp
}

func getLedger(w http.ResponseWriter, r *http.Request, d module.Deps) {
	leaseID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	ledger, err := GetLedger(r.Context(), d.Pool, p.OrgID, leaseID, today(d))
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, ledgerToResponse(ledger))
}

// --- rent-run handler ---

func runRent(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	result, err := RunRent(ctx, d.Billing, p.OrgID, r.PathValue("org"), today(d))
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, result)
}

// --- bond handlers ---

type bondInvoiceResponse struct {
	InvoiceID int64 `json:"invoice_id"`
	Total     int64 `json:"total"`
}

func createBondInvoiceHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	leaseID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	inv, err := CreateBondInvoice(ctx, d.Billing, p.OrgID, r.PathValue("org"), leaseID)
	if err != nil {
		if errors.Is(err, billing.ErrTooFewPayOptions) {
			module.WriteError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, bondInvoiceResponse{InvoiceID: inv.ID, Total: int64(inv.Total)})
}

type bondLodgementRequest struct {
	RTARef   string `json:"rta_ref"`
	LodgedOn string `json:"lodged_on"`
}

func createBondLodgementHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	leaseID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body bondLodgementRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	lodgedOn, err := parseDate(body.LodgedOn)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "lodged_on must be an RFC 3339 date (YYYY-MM-DD)")
		return
	}

	now := today(d)
	p := auth.From(r.Context())
	var updated Lease
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = LodgeBond(r.Context(), tx, p.OrgID, &p.UserID, leaseID, body.RTARef, lodgedOn, now)
		return err
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	status, warnings, err := BondInfo(r.Context(), d.Pool, p.OrgID, updated, now)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, leaseToResponse(updated, status, warnings, now))
}
