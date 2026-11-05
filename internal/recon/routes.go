package recon

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
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
	return module.Module{Name: "reconciliation", Routes: registerRoutes}
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

type bankLineResponse struct {
	ID            int64   `json:"id"`
	BankAccountID int64   `json:"bank_account_id"`
	ImportID      int64   `json:"import_id"`
	PostedOn      string  `json:"posted_on"`
	Amount        int64   `json:"amount"`
	Description   string  `json:"description"`
	Status        string  `json:"status"`
	IgnoreReason  *string `json:"ignore_reason,omitempty"`
}

func bankLineToResponse(l BankLine) bankLineResponse {
	return bankLineResponse{
		ID: l.ID, BankAccountID: l.BankAccountID, ImportID: l.ImportID,
		PostedOn: l.PostedOn.Format(dateLayout), Amount: int64(l.Amount),
		Description: l.Description, Status: l.Status, IgnoreReason: l.IgnoreReason,
	}
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
	result, err := Import(r.Context(), d.Pool, p.OrgID, accountID, file)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, importResponse{RowsTotal: result.RowsTotal, RowsNew: result.RowsNew})
}

var validBankLineStatuses = map[string]bool{"unmatched": true, "matched": true, "ignored": true}

func listBankLinesHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	q := r.URL.Query()
	var filter BankLineFilter

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

	p := auth.From(r.Context())
	lines, err := ListBankLines(r.Context(), d.Pool, p.OrgID, filter)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]bankLineResponse, len(lines))
	for i, l := range lines {
		resp[i] = bankLineToResponse(l)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}
