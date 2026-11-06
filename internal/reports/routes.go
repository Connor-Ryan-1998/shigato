package reports

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

// dateLayout matches httpapi's own date encoding (YYYY-MM-DD), the same
// constant every other module defines for its own routes.
const dateLayout = "2006-01-02"

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation(dateLayout, s, time.UTC)
}

// maxRangeDays bounds from/to: a range longer than this is rejected, so a
// report can never be asked to scan an unbounded slice of history.
const maxRangeDays = 366

// resolveRange validates and returns [from, to] from q's "from"/"to"
// params: both must parse as dates, to must not be before from, and the
// range must not exceed maxRangeDays. The returned bool is false (with a
// client-facing message) on any violation.
func resolveRange(q map[string][]string) (from, to time.Time, msg string, ok bool) {
	fromS := first(q, "from")
	toS := first(q, "to")
	if fromS == "" || toS == "" {
		return time.Time{}, time.Time{}, "from and to are required (YYYY-MM-DD)", false
	}
	from, err := parseDate(fromS)
	if err != nil {
		return time.Time{}, time.Time{}, "from must be YYYY-MM-DD", false
	}
	to, err = parseDate(toS)
	if err != nil {
		return time.Time{}, time.Time{}, "to must be YYYY-MM-DD", false
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, "to must not be before from", false
	}
	// Inclusive day count: both from and to count as days in range, so
	// 2026-07-01..2026-07-01 is 1 day, and 2026-07-01..2027-07-02 is 367
	// (366 days apart plus the first day) - over the 366-day limit.
	inclusiveDays := int(to.Sub(from).Hours()/24) + 1
	if inclusiveDays > maxRangeDays {
		return time.Time{}, time.Time{}, fmt.Sprintf("range must not exceed %d days", maxRangeDays), false
	}
	return from, to, "", true
}

func first(q map[string][]string, key string) string {
	v := q[key]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// RegisterRoutes mounts the reports routes. Reports are core: always on,
// not gated by config.Config.Modules, so httpapi.New calls this directly
// rather than listing it in the modules registry.
func RegisterRoutes(mux *http.ServeMux, d module.Deps) {
	mux.Handle("GET /api/v1/orgs/{org}/reports/pnl",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pnlHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/reports/bas",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			basHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/reports/bas/detail",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			basDetailHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/reports/transactions.csv",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			transactionsCSVHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/reports/pnl.csv",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pnlCSVHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/reports/bas.csv",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			basCSVHandler(w, r, d)
		})))
}

// --- shared helpers ---

// orgFYStartMonth reads orgID's fy_start_month directly (not exposed on
// core.OrgSettings).
func orgFYStartMonth(ctx context.Context, q core.Querier, orgID int64) (int, error) {
	var m int
	err := q.QueryRow(ctx, `SELECT fy_start_month FROM orgs WHERE id = $1`, orgID).Scan(&m)
	return m, err
}

// rangeFromRequest resolves [from, to] for a report request: either an
// explicit from/to pair, or (for /bas and /bas.csv) a period=FY2027-Q1
// style param resolved against the org's fy_start_month.
func rangeFromRequest(ctx context.Context, q core.Querier, orgID int64, r *http.Request) (from, to time.Time, period, msg string, ok bool) {
	query := r.URL.Query()
	if p := query.Get("period"); p != "" {
		fyStartMonth, err := orgFYStartMonth(ctx, q, orgID)
		if err != nil {
			return time.Time{}, time.Time{}, "", "internal error", false
		}
		from, to, err = FYQuarter(p, fyStartMonth)
		if err != nil {
			return time.Time{}, time.Time{}, "", err.Error(), false
		}
		return from, to, p, "", true
	}
	from, to, msg, ok = resolveRange(query)
	return from, to, "", msg, ok
}

func propertyNames(ctx context.Context, q core.Querier, orgID int64) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT id, name FROM properties WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[strconv.FormatInt(id, 10)] = name
	}
	return out, rows.Err()
}

func projectNames(ctx context.Context, q core.Querier, orgID int64) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT id, name FROM projects WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[strconv.FormatInt(id, 10)] = name
	}
	return out, rows.Err()
}

// nameOf resolves an id (as rendered by groupKey/propertyNameFor) to a
// property or project name. "Unassigned" and "org" sentinel keys pass
// through unchanged, and an id with no matching row (shouldn't happen,
// since the id came from a live FK) falls back to itself.
func nameOf(names map[string]string, key string) string {
	if key == unassignedKey {
		return key
	}
	if name, ok := names[key]; ok {
		return name
	}
	return key
}

// resolveGroupKeys replaces each group's Key (an id, "Unassigned", or the
// orgGroupKey sentinel) with its display name: the org's name for groupBy
// "org", or a property/project's name looked up directly from its table.
func resolveGroupKeys(ctx context.Context, q core.Querier, orgID int64, groupBy string, groups []PnLGroup) ([]PnLGroup, error) {
	switch groupBy {
	case "property":
		names, err := propertyNames(ctx, q, orgID)
		if err != nil {
			return nil, err
		}
		for i := range groups {
			groups[i].Key = nameOf(names, groups[i].Key)
		}
	case "project":
		names, err := projectNames(ctx, q, orgID)
		if err != nil {
			return nil, err
		}
		for i := range groups {
			groups[i].Key = nameOf(names, groups[i].Key)
		}
	default:
		var orgName string
		if err := q.QueryRow(ctx, `SELECT name FROM orgs WHERE id = $1`, orgID).Scan(&orgName); err != nil {
			return nil, err
		}
		for i := range groups {
			if groups[i].Key == orgGroupKey {
				groups[i].Key = orgName
			}
		}
	}
	return groups, nil
}

// --- /pnl ---

type pnlGroupResponse struct {
	Key           string           `json:"key"`
	Income        map[string]int64 `json:"income"`
	Expenses      map[string]int64 `json:"expenses"`
	TotalIncome   int64            `json:"total_income"`
	TotalExpenses int64            `json:"total_expenses"`
	Net           int64            `json:"net"`
}

func pnlGroupToResponse(g PnLGroup) pnlGroupResponse {
	income := make(map[string]int64, len(g.Income))
	for k, v := range g.Income {
		income[k] = int64(v)
	}
	expenses := make(map[string]int64, len(g.Expenses))
	for k, v := range g.Expenses {
		expenses[k] = int64(v)
	}
	return pnlGroupResponse{
		Key: g.Key, Income: income, Expenses: expenses,
		TotalIncome: int64(g.TotalIncome), TotalExpenses: int64(g.TotalExpenses), Net: int64(g.Net),
	}
}

func validGroupBy(s string) bool {
	switch s {
	case "org", "property", "project":
		return true
	default:
		return false
	}
}

// pnlRows computes orgID's P&L groups for [from, to] and groupBy, with
// group keys resolved to display names.
func pnlRows(ctx context.Context, q core.Querier, orgID int64, from, to time.Time, groupBy string) ([]PnLGroup, error) {
	income, err := IncomeRows(ctx, q, orgID, from, to)
	if err != nil {
		return nil, err
	}
	exp, err := ExpenseRows(ctx, q, orgID, from, to)
	if err != nil {
		return nil, err
	}
	groups := PnL(income, exp, groupBy)
	return resolveGroupKeys(ctx, q, orgID, groupBy, groups)
}

func pnlHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	from, to, msg, ok := resolveRange(r.URL.Query())
	if !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "org"
	}
	if !validGroupBy(groupBy) {
		module.WriteError(w, http.StatusUnprocessableEntity, "group_by must be org, property or project")
		return
	}

	groups, err := pnlRows(r.Context(), d.Pool, p.OrgID, from, to, groupBy)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]pnlGroupResponse, len(groups))
	for i, g := range groups {
		resp[i] = pnlGroupToResponse(g)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

// --- /bas and /bas/detail ---

// requireGSTRegistered loads orgID's settings and writes a 422 if the org
// isn't GST-registered, per the BAS spec. ok is false if the response was
// already written.
func requireGSTRegistered(w http.ResponseWriter, ctx context.Context, q core.Querier, orgID int64) bool {
	settings, err := core.GetSettings(ctx, q, orgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return false
	}
	if !settings.GSTRegistered {
		module.WriteError(w, http.StatusUnprocessableEntity, "org is not GST-registered")
		return false
	}
	return true
}

type basResponse struct {
	Period         string     `json:"period,omitempty"`
	FiguresCents   BASFigures `json:"figures_cents"`
	FiguresDollars BASDollars `json:"figures_dollars"`
}

func basHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	if !requireGSTRegistered(w, r.Context(), d.Pool, p.OrgID) {
		return
	}
	from, to, period, msg, ok := rangeFromRequest(r.Context(), d.Pool, p.OrgID, r)
	if !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	income, err := IncomeRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	exp, err := ExpenseRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	figures := BAS(income, exp)
	module.WriteJSON(w, http.StatusOK, basResponse{Period: period, FiguresCents: figures, FiguresDollars: figures.Dollars()})
}

var validBASFields = map[string]bool{"G1": true, "G3": true, "G4": true, "G10": true, "G11": true, "1A": true, "1B": true}

// incomeRowResponse is IncomeRow's wire shape: snake_case keys and a
// YYYY-MM-DD date, rather than IncomeRow's Go field names and time.Time's
// default RFC 3339 encoding.
type incomeRowResponse struct {
	PaymentID     int64  `json:"payment_id"`
	InvoiceID     int64  `json:"invoice_id"`
	InvoiceNumber *int   `json:"invoice_number,omitempty"`
	PaidOn        string `json:"paid_on"`
	Category      string `json:"category"`
	GSTCode       string `json:"gst_code"`
	InclGST       int64  `json:"incl_gst"`
	GST           int64  `json:"gst"`
	ExGST         int64  `json:"ex_gst"`
	PropertyID    *int64 `json:"property_id,omitempty"`
	ProjectID     *int64 `json:"project_id,omitempty"`
}

func incomeRowToResponse(r IncomeRow) incomeRowResponse {
	return incomeRowResponse{
		PaymentID: r.PaymentID, InvoiceID: r.InvoiceID, InvoiceNumber: r.InvoiceNumber,
		PaidOn: r.PaidOn.Format(dateLayout), Category: r.Category, GSTCode: string(r.Code),
		InclGST: int64(r.Incl), GST: int64(r.GST), ExGST: int64(r.Ex),
		PropertyID: r.PropertyID, ProjectID: r.ProjectID,
	}
}

// expenseRowResponse is ExpenseRow's wire shape, the same treatment as
// incomeRowResponse.
type expenseRowResponse struct {
	ExpenseID  int64  `json:"expense_id"`
	LineID     int64  `json:"line_id"`
	SpentOn    string `json:"spent_on"`
	Category   string `json:"category"`
	Capital    bool   `json:"capital"`
	GSTCode    string `json:"gst_code"`
	InclGST    int64  `json:"incl_gst"`
	GST        int64  `json:"gst"`
	ExGST      int64  `json:"ex_gst"`
	PropertyID *int64 `json:"property_id,omitempty"`
	ProjectID  *int64 `json:"project_id,omitempty"`
}

func expenseRowToResponse(r ExpenseRow) expenseRowResponse {
	return expenseRowResponse{
		ExpenseID: r.ExpenseID, LineID: r.LineID, SpentOn: r.SpentOn.Format(dateLayout),
		Category: r.Category, Capital: r.Capital, GSTCode: string(r.Code),
		InclGST: int64(r.Incl), GST: int64(r.GST), ExGST: int64(r.Ex),
		PropertyID: r.PropertyID, ProjectID: r.ProjectID,
	}
}

type basDetailResponse struct {
	Field    string               `json:"field"`
	Income   []incomeRowResponse  `json:"income,omitempty"`
	Expenses []expenseRowResponse `json:"expenses,omitempty"`
}

func basDetailHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	if !requireGSTRegistered(w, r.Context(), d.Pool, p.OrgID) {
		return
	}
	field := r.URL.Query().Get("field")
	if !validBASFields[field] {
		module.WriteError(w, http.StatusUnprocessableEntity, "field must be one of G1, G3, G4, G10, G11, 1A, 1B")
		return
	}
	from, to, _, msg, ok := rangeFromRequest(r.Context(), d.Pool, p.OrgID, r)
	if !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	income, err := IncomeRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	exp, err := ExpenseRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	incomeRows, expenseRows, err := BASDetail(income, exp, field)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	incomeResp := make([]incomeRowResponse, len(incomeRows))
	for i, r := range incomeRows {
		incomeResp[i] = incomeRowToResponse(r)
	}
	expenseResp := make([]expenseRowResponse, len(expenseRows))
	for i, r := range expenseRows {
		expenseResp[i] = expenseRowToResponse(r)
	}
	module.WriteJSON(w, http.StatusOK, basDetailResponse{Field: field, Income: incomeResp, Expenses: expenseResp})
}

// --- CSV ---

// csvSafe guards against formula injection by prefixing a single quote to
// any text cell starting with =, +, -, @, tab or carriage return, which
// spreadsheet software would otherwise treat as a formula. Only applied to free text cells
// (descriptions/categories/references/names), never to the numeric amount
// columns.
func csvSafe(s string) string {
	if s != "" && strings.ContainsAny(s[:1], "=+-@\t\r") {
		return "'" + s
	}
	return s
}

// formatCents renders c as dollars with 2 decimal places, e.g. 11000 ->
// "110.00", -500 -> "-5.00".
func formatCents(c core.Cents) string {
	neg := c < 0
	if neg {
		c = -c
	}
	s := fmt.Sprintf("%d.%02d", int64(c)/100, int64(c)%100)
	if neg {
		s = "-" + s
	}
	return s
}

func writeCSV(w http.ResponseWriter, filename string, rows [][]string) error {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	for _, row := range rows {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func transactionsCSVHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	from, to, msg, ok := resolveRange(r.URL.Query())
	if !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	income, err := IncomeRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	exp, err := ExpenseRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	properties, err := propertyNames(r.Context(), d.Pool, p.OrgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	projects, err := projectNames(r.Context(), d.Pool, p.OrgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}

	rows := [][]string{{"date", "type", "category", "gst_code", "incl_gst", "gst", "ex_gst", "property", "project", "reference"}}
	for _, row := range income {
		reference := ""
		if row.InvoiceNumber != nil {
			reference = strconv.Itoa(*row.InvoiceNumber)
		}
		rows = append(rows, []string{
			row.PaidOn.Format(dateLayout), "income", csvSafe(row.Category), string(row.Code),
			formatCents(row.Incl), formatCents(row.GST), formatCents(row.Ex),
			csvSafe(propertyIDName(properties, row.PropertyID)), csvSafe(projectIDName(projects, row.ProjectID)),
			csvSafe(reference),
		})
	}
	for _, row := range exp {
		rows = append(rows, []string{
			row.SpentOn.Format(dateLayout), "expense", csvSafe(row.Category), string(row.Code),
			formatCents(row.Incl), formatCents(row.GST), formatCents(row.Ex),
			csvSafe(propertyIDName(properties, row.PropertyID)), csvSafe(projectIDName(projects, row.ProjectID)),
			csvSafe(strconv.FormatInt(row.ExpenseID, 10)),
		})
	}

	if err := writeCSV(w, "transactions.csv", rows); err != nil {
		// The response may already be partially written (headers sent,
		// some CSV rows flushed), so writing another status/body here
		// would be a no-op at best and corrupt the response at worst;
		// log it instead.
		slog.Error("writing transactions.csv failed", "error", err)
	}
}

func propertyIDName(names map[string]string, id *int64) string {
	if id == nil {
		return ""
	}
	return nameOf(names, strconv.FormatInt(*id, 10))
}

func projectIDName(names map[string]string, id *int64) string {
	if id == nil {
		return ""
	}
	return nameOf(names, strconv.FormatInt(*id, 10))
}

func pnlCSVHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	from, to, msg, ok := resolveRange(r.URL.Query())
	if !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "org"
	}
	if !validGroupBy(groupBy) {
		module.WriteError(w, http.StatusUnprocessableEntity, "group_by must be org, property or project")
		return
	}

	groups, err := pnlRows(r.Context(), d.Pool, p.OrgID, from, to, groupBy)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}

	rows := [][]string{{"group", "type", "category", "amount"}}
	for _, g := range groups {
		for _, cat := range sortedKeys(g.Income) {
			rows = append(rows, []string{csvSafe(g.Key), "income", csvSafe(cat), formatCents(g.Income[cat])})
		}
		for _, cat := range sortedKeys(g.Expenses) {
			rows = append(rows, []string{csvSafe(g.Key), "expense", csvSafe(cat), formatCents(g.Expenses[cat])})
		}
		rows = append(rows, []string{csvSafe(g.Key), "total", "Total income", formatCents(g.TotalIncome)})
		rows = append(rows, []string{csvSafe(g.Key), "total", "Total expenses", formatCents(g.TotalExpenses)})
		rows = append(rows, []string{csvSafe(g.Key), "total", "Net", formatCents(g.Net)})
	}

	if err := writeCSV(w, "pnl.csv", rows); err != nil {
		slog.Error("writing pnl.csv failed", "error", err)
	}
}

func sortedKeys(m map[string]core.Cents) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func basCSVHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	if !requireGSTRegistered(w, r.Context(), d.Pool, p.OrgID) {
		return
	}
	from, to, _, msg, ok := rangeFromRequest(r.Context(), d.Pool, p.OrgID, r)
	if !ok {
		module.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	income, err := IncomeRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	exp, err := ExpenseRows(r.Context(), d.Pool, p.OrgID, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	f := BAS(income, exp)
	dollars := f.Dollars()

	rows := [][]string{
		{"field", "cents", "dollars"},
		{"G1", strconv.FormatInt(int64(f.G1), 10), strconv.FormatInt(dollars.G1, 10)},
		{"G2", strconv.FormatInt(int64(f.G2), 10), strconv.FormatInt(dollars.G2, 10)},
		{"G3", strconv.FormatInt(int64(f.G3), 10), strconv.FormatInt(dollars.G3, 10)},
		{"G4", strconv.FormatInt(int64(f.G4), 10), strconv.FormatInt(dollars.G4, 10)},
		{"G10", strconv.FormatInt(int64(f.G10), 10), strconv.FormatInt(dollars.G10, 10)},
		{"G11", strconv.FormatInt(int64(f.G11), 10), strconv.FormatInt(dollars.G11, 10)},
		{"1A", strconv.FormatInt(int64(f.A1), 10), strconv.FormatInt(dollars.A1, 10)},
		{"1B", strconv.FormatInt(int64(f.B1), 10), strconv.FormatInt(dollars.B1, 10)},
	}

	if err := writeCSV(w, "bas.csv", rows); err != nil {
		slog.Error("writing bas.csv failed", "error", err)
	}
}
