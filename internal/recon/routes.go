package recon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/importers"
	"shigoto/internal/module"
)

// maxUploadBytes caps a bank-import multipart upload, enforced on the
// whole request body (not just the parsed file part) via
// http.MaxBytesReader.
const maxUploadBytes = 5 << 20 // 5 MiB

// dateLayout matches httpapi's own date encoding (YYYY-MM-DD), the same
// constant timesheets and property each define for their own routes.
const dateLayout = "2006-01-02"

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation(dateLayout, s, time.UTC)
}

// Module returns the reconciliation module: bank accounts and their CSV
// imports, gated by config.Config.Modules["reconciliation"].
func Module() module.Module {
	return module.Module{Name: "reconciliation", Routes: registerRoutes, Dashboard: dashboardSection}
}

// reconDashboard is the reconciliation module's dashboard section.
type reconDashboard struct {
	UnmatchedCount    int     `json:"unmatched_count"`
	OldestUnmatchedOn *string `json:"oldest_unmatched_on"`
	UnmatchedIn       int64   `json:"unmatched_in"`
	UnmatchedOut      int64   `json:"unmatched_out"`
}

// dashboardSection builds the reconciliation module's dashboard section in
// a single aggregate query over unmatched bank lines: how many, the oldest
// one's date, and the total credited in versus debited out.
func dashboardSection(ctx context.Context, q core.Querier, orgID int64, _ time.Time) (any, error) {
	var d reconDashboard
	var oldest *time.Time
	err := q.QueryRow(ctx, `
		SELECT count(*), min(posted_on),
			coalesce(sum(amount) FILTER (WHERE amount > 0), 0),
			coalesce(sum(-amount) FILTER (WHERE amount < 0), 0)
		FROM bank_lines WHERE org_id = $1 AND status = 'unmatched'`,
		orgID,
	).Scan(&d.UnmatchedCount, &oldest, &d.UnmatchedIn, &d.UnmatchedOut)
	if err != nil {
		return nil, err
	}
	if oldest != nil {
		s := oldest.Format(dateLayout)
		d.OldestUnmatchedOn = &s
	}
	return d, nil
}

func registerRoutes(mux *http.ServeMux, d module.Deps) {
	mux.Handle("POST /api/v1/orgs/{org}/bank-accounts",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createBankAccount(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/bank-accounts",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listBankAccounts(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/bank-accounts/{id}/imports",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			importBankLines(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/bank-lines",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listBankLinesHandler(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/categories",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createCategoryHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/categories",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listCategoriesHandler(w, r, d)
		})))
	mux.Handle("PUT /api/v1/orgs/{org}/categories/{id}",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			updateCategoryHandler(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/files",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uploadFileHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/files/{id}",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			downloadFileHandler(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/expenses",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createExpenseHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/expenses",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listExpensesHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/expenses/{id}",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getExpenseHandler(w, r, d)
		})))
	mux.Handle("DELETE /api/v1/orgs/{org}/expenses/{id}",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deleteExpenseHandler(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/bank-lines/{id}/match-invoice",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			matchInvoiceHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/bank-lines/{id}/match-expense",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			matchExpenseHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/bank-lines/{id}/match-payout",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			matchPayoutHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/bank-lines/{id}/expense",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createExpenseFromLineHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/bank-lines/{id}/ignore",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ignoreHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/bank-lines/{id}/unmatch",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			unmatchHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/bank-lines/{id}/suggestions",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			suggestionsHandler(w, r, d)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/reconcile/auto",
		d.Auth.RequireOrg(auth.PermReconWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			autoMatchHandler(w, r, d)
		})))
}

// --- request/response shapes ---

// bankAccountRequest is a create body: exactly one of Preset or CSVMapping
// must be set.
type bankAccountRequest struct {
	Name       string                `json:"name"`
	Preset     *string               `json:"preset,omitempty"`
	CSVMapping *importers.CSVMapping `json:"csv_mapping,omitempty"`
}

// mapping resolves body's CSVMapping, from the named preset or the literal
// mapping, enforcing that exactly one was given.
func (body bankAccountRequest) mapping() (importers.CSVMapping, error) {
	switch {
	case body.Preset != nil && body.CSVMapping != nil:
		return importers.CSVMapping{}, fmt.Errorf("%w: set exactly one of preset or csv_mapping", core.ErrInvalidInput)
	case body.Preset != nil:
		m, ok := importers.Preset(*body.Preset)
		if !ok {
			return importers.CSVMapping{}, fmt.Errorf("%w: unknown preset %q", core.ErrInvalidInput, *body.Preset)
		}
		return m, nil
	case body.CSVMapping != nil:
		return *body.CSVMapping, nil
	default:
		return importers.CSVMapping{}, fmt.Errorf("%w: set exactly one of preset or csv_mapping", core.ErrInvalidInput)
	}
}

type bankAccountResponse struct {
	ID         int64                `json:"id"`
	Name       string               `json:"name"`
	CSVMapping importers.CSVMapping `json:"csv_mapping"`
}

func bankAccountToResponse(a BankAccount) bankAccountResponse {
	return bankAccountResponse{ID: a.ID, Name: a.Name, CSVMapping: a.CSVMapping}
}

type importResponse struct {
	RowsTotal int `json:"rows_total"`
	RowsNew   int `json:"rows_new"`
}

// bankLineMatchResponse is what bank line's matched (or ignored) entity
// renders as: the invoice number for invoices, the expense description for
// expenses, the payout ref for payouts, or the ignore reason for ignored
// lines.
type bankLineMatchResponse struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

type bankLineResponse struct {
	ID            int64                  `json:"id"`
	BankAccountID int64                  `json:"bank_account_id"`
	ImportID      int64                  `json:"import_id"`
	PostedOn      string                 `json:"posted_on"`
	Amount        int64                  `json:"amount"`
	Description   string                 `json:"description"`
	Status        string                 `json:"status"`
	IgnoreReason  *string                `json:"ignore_reason,omitempty"`
	Match         *bankLineMatchResponse `json:"match"`
}

// bankLineToResponse builds l's JSON response. match is l's resolved
// BankLineMatch (see MatchFor), nil for an unmatched line - rendered as a
// JSON null, never omitted, so a client can always rely on the key being
// present.
func bankLineToResponse(l BankLine, match *BankLineMatch) bankLineResponse {
	resp := bankLineResponse{
		ID: l.ID, BankAccountID: l.BankAccountID, ImportID: l.ImportID,
		PostedOn: l.PostedOn.Format(dateLayout), Amount: int64(l.Amount),
		Description: l.Description, Status: l.Status, IgnoreReason: l.IgnoreReason,
	}
	if match != nil {
		resp.Match = &bankLineMatchResponse{Kind: match.Kind, ID: match.ID, Label: match.Label}
	}
	return resp
}

// --- handlers ---

func createBankAccount(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body bankAccountRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	mapping, err := body.mapping()
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}

	p := auth.From(r.Context())
	var created BankAccount
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateBankAccount(r.Context(), tx, p.OrgID, body.Name, mapping)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "bank_account", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, bankAccountToResponse(created))
}

func listBankAccounts(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	accounts, err := ListBankAccounts(r.Context(), d.Pool, p.OrgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]bankAccountResponse, len(accounts))
	for i, a := range accounts {
		resp[i] = bankAccountToResponse(a)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

// importBankLines handles the multipart CSV upload. The request body is
// capped at maxUploadBytes via http.MaxBytesReader; exceeding it gives 413,
// checked as its own case (a *http.MaxBytesError) rather than folded into
// the generic "bad multipart body" 400, since a too-large upload isn't a
// malformed one.
func importBankLines(w http.ResponseWriter, r *http.Request, d module.Deps) {
	accountID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			module.WriteError(w, http.StatusRequestEntityTooLarge, "file too large")
			return
		}
		module.WriteError(w, http.StatusBadRequest, "invalid multipart body")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "file is required")
		return
	}
	defer file.Close()

	p := auth.From(r.Context())
	result, err := Import(billing.WithActor(r.Context(), p.UserID), d.Pool, p.OrgID, accountID, file)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, importResponse{RowsTotal: result.RowsTotal, RowsNew: result.RowsNew})
}

var validBankLineStatuses = map[string]bool{"unmatched": true, "matched": true, "ignored": true}

// defaultBankLineListLimit and maxBankLineListLimit bound GET /bank-lines'
// limit query param, matching GET /invoices' own defaults.
const (
	defaultBankLineListLimit = 50
	maxBankLineListLimit     = 200
)

// bankLineListResponse is GET /bank-lines' response shape: the page of
// lines plus the total count of lines matching the filter (ignoring
// limit/offset), for pagination.
type bankLineListResponse struct {
	Lines []bankLineResponse `json:"lines"`
	Total int                `json:"total"`
}

func listBankLinesHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	q := r.URL.Query()
	filter := BankLineFilter{Limit: defaultBankLineListLimit}

	if s := q.Get("account_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "account_id must be an integer")
			return
		}
		filter.BankAccountID = &id
	}
	if s := q.Get("status"); s != "" {
		if !validBankLineStatuses[s] {
			module.WriteError(w, http.StatusUnprocessableEntity, "status must be one of unmatched, matched, ignored")
			return
		}
		filter.Status = &s
	}
	if s := q.Get("from"); s != "" {
		t, err := parseDate(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "from must be YYYY-MM-DD")
			return
		}
		filter.From = &t
	}
	if s := q.Get("to"); s != "" {
		t, err := parseDate(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "to must be YYYY-MM-DD")
			return
		}
		filter.To = &t
	}
	if s := q.Get("limit"); s != "" {
		limit, err := strconv.Atoi(s)
		if err != nil || limit < 1 {
			module.WriteError(w, http.StatusUnprocessableEntity, "limit must be a positive integer")
			return
		}
		filter.Limit = min(limit, maxBankLineListLimit)
	}
	if s := q.Get("offset"); s != "" {
		offset, err := strconv.Atoi(s)
		if err != nil || offset < 0 {
			module.WriteError(w, http.StatusUnprocessableEntity, "offset must be a non-negative integer")
			return
		}
		filter.Offset = offset
	}

	p := auth.From(r.Context())
	lines, total, err := ListBankLines(r.Context(), d.Pool, p.OrgID, filter)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := bankLineListResponse{Lines: make([]bankLineResponse, len(lines)), Total: total}
	for i, l := range lines {
		match, err := MatchFor(r.Context(), d.Pool, p.OrgID, l)
		if err != nil {
			module.WriteCoreError(w, err)
			return
		}
		resp.Lines[i] = bankLineToResponse(l, match)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

// --- categories ---

// categoryRequest is both the create body and the strict-PUT update body:
// every field is given on every call, so a PUT fully replaces the row.
type categoryRequest struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	DefaultGSTCode string `json:"default_gst_code,omitempty"`
	Capital        bool   `json:"capital"`
}

type categoryResponse struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	DefaultGSTCode string `json:"default_gst_code,omitempty"`
	Capital        bool   `json:"capital"`
}

func categoryToResponse(c Category) categoryResponse {
	return categoryResponse{ID: c.ID, Name: c.Name, Type: c.Type, DefaultGSTCode: string(c.DefaultGSTCode), Capital: c.Capital}
}

func createCategoryHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body categoryRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := auth.From(r.Context())
	var created Category
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = CreateCategory(r.Context(), tx, p.OrgID, body.Name, body.Type, core.GSTCode(body.DefaultGSTCode), body.Capital)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "category", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, categoryToResponse(created))
}

func listCategoriesHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	categories, err := ListCategories(r.Context(), d.Pool, p.OrgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]categoryResponse, len(categories))
	for i, c := range categories {
		resp[i] = categoryToResponse(c)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func updateCategoryHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body categoryRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := auth.From(r.Context())
	var updated Category
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		updated, err = UpdateCategory(r.Context(), tx, p.OrgID, id, body.Name, body.Type, core.GSTCode(body.DefaultGSTCode), body.Capital)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "category", updated.ID, "update", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, categoryToResponse(updated))
}

// --- files ---

func uploadFileHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	created, ok := module.HandleUpload(w, r, d, nil)
	if !ok {
		return
	}
	module.WriteJSON(w, http.StatusCreated, module.FileToResponse(created))
}

func downloadFileHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	f, err := core.GetFile(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.ServeFile(w, f, d.Cfg.FilesDir)
}

// --- expenses ---

type expenseLineRequest struct {
	CategoryID int64   `json:"category_id"`
	Amount     int64   `json:"amount"`
	GSTCode    *string `json:"gst_code,omitempty"`
	PropertyID *int64  `json:"property_id,omitempty"`
	ProjectID  *int64  `json:"project_id,omitempty"`
}

type expenseRequest struct {
	SpentOn           string               `json:"spent_on"`
	SupplierContactID *int64               `json:"supplier_contact_id,omitempty"`
	Description       string               `json:"description"`
	ReceiptFileID     *int64               `json:"receipt_file_id,omitempty"`
	Lines             []expenseLineRequest `json:"lines"`
}

// toNewExpenseLines converts the shared expenseLineRequest shape into
// NewExpenseLine, used by both expenseRequest.toNewExpense (spent_on
// required) and expenseFromLineRequest.toNewExpense (spent_on optional).
func toNewExpenseLines(reqs []expenseLineRequest) ([]NewExpenseLine, error) {
	lines := make([]NewExpenseLine, len(reqs))
	for i, l := range reqs {
		var code *core.GSTCode
		if l.GSTCode != nil {
			parsed, err := core.ParseGSTCode(*l.GSTCode)
			if err != nil {
				return nil, fmt.Errorf("%w: line %d: %v", core.ErrInvalidInput, i, err)
			}
			code = &parsed
		}
		lines[i] = NewExpenseLine{
			CategoryID: l.CategoryID, Amount: core.Cents(l.Amount), GSTCode: code,
			PropertyID: l.PropertyID, ProjectID: l.ProjectID,
		}
	}
	return lines, nil
}

func (body expenseRequest) toNewExpense() (NewExpense, error) {
	spentOn, err := parseDate(body.SpentOn)
	if err != nil {
		return NewExpense{}, fmt.Errorf("%w: spent_on must be YYYY-MM-DD", core.ErrInvalidInput)
	}

	lines, err := toNewExpenseLines(body.Lines)
	if err != nil {
		return NewExpense{}, err
	}

	return NewExpense{
		SpentOn: spentOn, SupplierContactID: body.SupplierContactID,
		Description: body.Description, ReceiptFileID: body.ReceiptFileID, Lines: lines,
	}, nil
}

type expenseLineResponse struct {
	ID         int64  `json:"id"`
	CategoryID int64  `json:"category_id"`
	Amount     int64  `json:"amount"`
	GSTCode    string `json:"gst_code"`
	GSTAmount  int64  `json:"gst_amount"`
	PropertyID *int64 `json:"property_id,omitempty"`
	ProjectID  *int64 `json:"project_id,omitempty"`
}

type expenseResponse struct {
	ID                int64                 `json:"id"`
	SpentOn           string                `json:"spent_on"`
	SupplierContactID *int64                `json:"supplier_contact_id,omitempty"`
	Description       string                `json:"description"`
	ReceiptFileID     *int64                `json:"receipt_file_id,omitempty"`
	BankLineID        *int64                `json:"bank_line_id,omitempty"`
	Total             int64                 `json:"total"`
	Lines             []expenseLineResponse `json:"lines"`
}

func expenseToResponse(e Expense) expenseResponse {
	lines := make([]expenseLineResponse, len(e.Lines))
	var total core.Cents
	for i, l := range e.Lines {
		lines[i] = expenseLineResponse{
			ID: l.ID, CategoryID: l.CategoryID, Amount: int64(l.Amount),
			GSTCode: string(l.GSTCode), GSTAmount: int64(l.GSTAmount),
			PropertyID: l.PropertyID, ProjectID: l.ProjectID,
		}
		total += l.Amount
	}
	return expenseResponse{
		ID: e.ID, SpentOn: e.SpentOn.Format(dateLayout), SupplierContactID: e.SupplierContactID,
		Description: e.Description, ReceiptFileID: e.ReceiptFileID, BankLineID: e.BankLineID,
		Total: int64(total), Lines: lines,
	}
}

type createExpenseResponse struct {
	expenseResponse
	Warnings []string `json:"warnings"`
}

func createExpenseHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	var body expenseRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in, err := body.toNewExpense()
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}

	p := auth.From(r.Context())
	var created Expense
	var warnings []string
	err = module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, warnings, err = CreateExpense(r.Context(), tx, p.OrgID, in)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "expense", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, createExpenseResponse{expenseResponse: expenseToResponse(created), Warnings: warnings})
}

func getExpenseHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	e, err := GetExpense(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, expenseToResponse(e))
}

func listExpensesHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	q := r.URL.Query()
	var filter ExpenseFilter

	if s := q.Get("from"); s != "" {
		t, err := parseDate(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "from must be YYYY-MM-DD")
			return
		}
		filter.From = &t
	}
	if s := q.Get("to"); s != "" {
		t, err := parseDate(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "to must be YYYY-MM-DD")
			return
		}
		filter.To = &t
	}
	if s := q.Get("category_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "category_id must be an integer")
			return
		}
		filter.CategoryID = &id
	}
	if s := q.Get("property_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "property_id must be an integer")
			return
		}
		filter.PropertyID = &id
	}
	if s := q.Get("project_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "project_id must be an integer")
			return
		}
		filter.ProjectID = &id
	}
	if s := q.Get("unlinked"); s != "" {
		v, err := strconv.ParseBool(s)
		if err != nil {
			module.WriteError(w, http.StatusUnprocessableEntity, "unlinked must be a boolean")
			return
		}
		filter.Unlinked = v
	}

	p := auth.From(r.Context())
	expenses, err := ListExpenses(r.Context(), d.Pool, p.OrgID, filter)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]expenseResponse, len(expenses))
	for i, e := range expenses {
		resp[i] = expenseToResponse(e)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func deleteExpenseHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	err := module.RunInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		return DeleteExpense(ctx, tx, p.OrgID, id)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- matching ---

type matchInvoiceRequest struct {
	InvoiceID int64  `json:"invoice_id"`
	Method    string `json:"method"`
}

func matchInvoiceHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body matchInvoiceRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	var invResp billing.InvoiceResponse
	err := module.RunInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := MatchInvoice(ctx, tx, p.OrgID, lineID, body.InvoiceID, body.Method); err != nil {
			return err
		}
		inv, err := core.GetInvoice(ctx, tx, p.OrgID, body.InvoiceID)
		if err != nil {
			return err
		}
		invResp, err = d.Billing.InvoiceJSON(ctx, tx, inv)
		return err
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, invResp)
}

type matchExpenseRequest struct {
	ExpenseID int64 `json:"expense_id"`
}

func matchExpenseHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body matchExpenseRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	var updated Expense
	err := module.RunInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := MatchExpense(ctx, tx, p.OrgID, lineID, body.ExpenseID); err != nil {
			return err
		}
		var err error
		updated, err = GetExpense(ctx, tx, p.OrgID, body.ExpenseID)
		return err
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, expenseToResponse(updated))
}

type matchPayoutRequest struct {
	PayoutRef string `json:"payout_ref"`
}

type matchPayoutResponse struct {
	PaymentsLinked int    `json:"payments_linked"`
	Fees           int64  `json:"fees"`
	FeeExpenseID   *int64 `json:"fee_expense_id,omitempty"`
}

func matchPayoutHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body matchPayoutRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	result, err := MatchPayout(ctx, d.Pool, d.Billing, p.OrgID, r.PathValue("org"), lineID, body.PayoutRef)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, matchPayoutResponse{
		PaymentsLinked: result.PaymentsLinked, Fees: int64(result.Fees), FeeExpenseID: result.FeeExpenseID,
	})
}

// expenseFromLineRequest is the POST .../expense body: the same shape as
// expenseRequest, except SpentOn is optional - CreateExpenseFromLine
// defaults it to the bank line's posted_on when omitted.
type expenseFromLineRequest struct {
	SpentOn           *string              `json:"spent_on,omitempty"`
	SupplierContactID *int64               `json:"supplier_contact_id,omitempty"`
	Description       string               `json:"description"`
	ReceiptFileID     *int64               `json:"receipt_file_id,omitempty"`
	Lines             []expenseLineRequest `json:"lines"`
}

func (body expenseFromLineRequest) toNewExpense() (NewExpense, error) {
	var spentOn time.Time
	if body.SpentOn != nil {
		t, err := parseDate(*body.SpentOn)
		if err != nil {
			return NewExpense{}, fmt.Errorf("%w: spent_on must be YYYY-MM-DD", core.ErrInvalidInput)
		}
		spentOn = t
	}

	lines, err := toNewExpenseLines(body.Lines)
	if err != nil {
		return NewExpense{}, err
	}

	return NewExpense{
		SpentOn: spentOn, SupplierContactID: body.SupplierContactID,
		Description: body.Description, ReceiptFileID: body.ReceiptFileID, Lines: lines,
	}, nil
}

func createExpenseFromLineHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body expenseFromLineRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in, err := body.toNewExpense()
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	var created Expense
	var warnings []string
	err = module.RunInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		var err error
		created, warnings, err = CreateExpenseFromLine(ctx, tx, p.OrgID, lineID, in)
		return err
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, createExpenseResponse{expenseResponse: expenseToResponse(created), Warnings: warnings})
}

type ignoreRequest struct {
	Reason string `json:"reason"`
}

func ignoreHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var body ignoreRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	var updated BankLine
	var match *BankLineMatch
	err := module.RunInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := Ignore(ctx, tx, p.OrgID, lineID, body.Reason); err != nil {
			return err
		}
		var err error
		updated, err = getBankLine(ctx, tx, p.OrgID, lineID)
		if err != nil {
			return err
		}
		match, err = MatchFor(ctx, tx, p.OrgID, updated)
		return err
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, bankLineToResponse(updated, match))
}

func unmatchHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	var updated BankLine
	err := module.RunInTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := Unmatch(ctx, tx, p.OrgID, lineID); err != nil {
			return err
		}
		var err error
		updated, err = getBankLine(ctx, tx, p.OrgID, lineID)
		return err
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	// Unmatch always reverts to "unmatched", which never has a match; no
	// MatchFor lookup needed here (unlike ignoreHandler above).
	module.WriteJSON(w, http.StatusOK, bankLineToResponse(updated, nil))
}

type suggestionResponse struct {
	Kind   string `json:"kind"`
	ID     int64  `json:"id,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Score  int    `json:"score"`
	Reason string `json:"reason"`
}

func suggestionsHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	lineID, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	suggestions, err := Suggest(r.Context(), d.Pool, d.Billing, p.OrgID, r.PathValue("org"), lineID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]suggestionResponse, len(suggestions))
	for i, s := range suggestions {
		resp[i] = suggestionResponse{Kind: s.Kind, ID: s.ID, Ref: s.Ref, Score: s.Score, Reason: s.Reason}
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

type autoMatchResponse struct {
	Matched int `json:"matched"`
}

func autoMatchHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	p := auth.From(r.Context())
	ctx := billing.WithActor(r.Context(), p.UserID)
	matched, err := AutoMatch(ctx, d.Pool, p.OrgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, autoMatchResponse{Matched: matched})
}
