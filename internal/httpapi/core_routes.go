package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
)

// registerCoreRoutes mounts the contacts, invoices and payments routes.
// Unlike the modules in the modules registry, these are always on: they
// aren't gated by config.Config.Modules.
func registerCoreRoutes(mux *http.ServeMux, d Deps) {
	getenv := d.GetEnv

	mux.Handle("POST /api/v1/orgs/{org}/contacts",
		d.Auth.RequireOrg(auth.PermContactsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createContact(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/contacts",
		d.Auth.RequireOrg(auth.PermInvoicesRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listContacts(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/invoices",
		d.Auth.RequireOrg(auth.PermInvoicesWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createInvoice(w, r, d, getenv)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/invoices/{id}",
		d.Auth.RequireOrg(auth.PermInvoicesRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getInvoice(w, r, d, getenv)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/invoices",
		d.Auth.RequireOrg(auth.PermInvoicesRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listInvoices(w, r, d, getenv)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/invoices/{id}/issue",
		d.Auth.RequireOrg(auth.PermInvoicesWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			issueInvoice(w, r, d, getenv)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/invoices/{id}/void",
		d.Auth.RequireOrg(auth.PermInvoicesWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			voidInvoice(w, r, d, getenv)
		})))
	mux.Handle("POST /api/v1/orgs/{org}/invoices/{id}/payments",
		d.Auth.RequireOrg(auth.PermPaymentsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recordPayment(w, r, d)
		})))
}

// --- request/response shapes ---

type contactRequest struct {
	Kind                string `json:"kind"`
	Name                string `json:"name"`
	Email               string `json:"email,omitempty"`
	Phone               string `json:"phone,omitempty"`
	CompanyName         string `json:"company_name,omitempty"`
	ABN                 string `json:"abn,omitempty"`
	ProviderCustomerRef string `json:"provider_customer_ref,omitempty"`
}

type contactResponse struct {
	ID                  int64  `json:"id"`
	Kind                string `json:"kind"`
	Name                string `json:"name"`
	Email               string `json:"email,omitempty"`
	Phone               string `json:"phone,omitempty"`
	CompanyName         string `json:"company_name,omitempty"`
	ABN                 string `json:"abn,omitempty"`
	ProviderCustomerRef string `json:"provider_customer_ref,omitempty"`
}

func contactToResponse(c core.Contact) contactResponse {
	return contactResponse{
		ID: c.ID, Kind: c.Kind, Name: c.Name, Email: c.Email, Phone: c.Phone,
		CompanyName: c.CompanyName, ABN: c.ABN, ProviderCustomerRef: c.ProviderCustomerRef,
	}
}

type lineRequest struct {
	Description   string `json:"description"`
	QuantityMilli int64  `json:"quantity_milli"`
	UnitAmount    int64  `json:"unit_amount"`
	GSTCode       string `json:"gst_code"`
}

type lineResponse struct {
	ID            int64  `json:"id"`
	Description   string `json:"description"`
	QuantityMilli int64  `json:"quantity_milli"`
	UnitAmount    int64  `json:"unit_amount"`
	GSTCode       string `json:"gst_code"`
	GSTAmount     int64  `json:"gst_amount"`
	Amount        int64  `json:"amount"`
}

type invoiceRequest struct {
	ContactID int64         `json:"contact_id"`
	Source    string        `json:"source"`
	DueInDays int           `json:"due_in_days"`
	Lines     []lineRequest `json:"lines"`
}

type invoiceResponse struct {
	ID               int64          `json:"id"`
	ContactID        int64          `json:"contact_id"`
	Number           *int           `json:"number"`
	Source           string         `json:"source"`
	Status           string         `json:"status"`
	IssueDate        *string        `json:"issue_date"`
	DueDate          *string        `json:"due_date"`
	Currency         string         `json:"currency"`
	Subtotal         int64          `json:"subtotal"`
	GSTTotal         int64          `json:"gst_total"`
	Total            int64          `json:"total"`
	PaymentReference string         `json:"payment_reference"`
	PayOptions       []string       `json:"pay_options"`
	Lines            []lineResponse `json:"lines"`
}

const dateLayout = "2006-01-02"

func invoiceToResponse(ctx context.Context, q core.Querier, getenv func(string) string, inv core.Invoice) (invoiceResponse, error) {
	opts, err := payOptionsFor(ctx, q, getenv, inv.OrgID)
	if err != nil {
		return invoiceResponse{}, err
	}

	resp := invoiceResponse{
		ID: inv.ID, ContactID: inv.ContactID, Number: inv.Number, Source: inv.Source, Status: inv.Status,
		Currency: inv.Currency, Subtotal: int64(inv.Subtotal), GSTTotal: int64(inv.GSTTotal), Total: int64(inv.Total),
		PaymentReference: inv.PaymentReference, PayOptions: opts, Lines: make([]lineResponse, len(inv.Lines)),
	}
	if inv.IssueDate != nil {
		s := inv.IssueDate.Format(dateLayout)
		resp.IssueDate = &s
	}
	if inv.DueDate != nil {
		s := inv.DueDate.Format(dateLayout)
		resp.DueDate = &s
	}
	for i, l := range inv.Lines {
		resp.Lines[i] = lineResponse{
			ID: l.ID, Description: l.Description, QuantityMilli: l.QuantityMilli, UnitAmount: int64(l.UnitAmount),
			GSTCode: string(l.GSTCode), GSTAmount: int64(l.GSTAmount), Amount: int64(l.Amount),
		}
	}
	return resp, nil
}

// payOptionsFor builds the pay_options an org currently offers: bank
// transfer when it has both a BSB and account number, PayID when it has
// one, and Stripe when config.StripeFor finds credentials for its slug
// (Task 7 wires actually charging through it).
func payOptionsFor(ctx context.Context, q core.Querier, getenv func(string) string, orgID int64) ([]string, error) {
	var slug string
	var bsb, account, payid sql.NullString
	err := q.QueryRow(ctx, `SELECT slug, bank_bsb, bank_account, payid FROM orgs WHERE id = $1`, orgID).
		Scan(&slug, &bsb, &account, &payid)
	if err != nil {
		return nil, err
	}

	opts := []string{}
	if bsb.String != "" && account.String != "" {
		opts = append(opts, "bank_transfer")
	}
	if payid.String != "" {
		opts = append(opts, "payid")
	}
	if _, ok := config.StripeFor(getenv, slug); ok {
		opts = append(opts, "stripe")
	}
	return opts, nil
}

type paymentRequest struct {
	Amount      int64  `json:"amount"`
	PaidOn      string `json:"paid_on"`
	Method      string `json:"method"`
	ProviderRef string `json:"provider_ref,omitempty"`
}

type paymentResponse struct {
	ID          int64  `json:"id"`
	InvoiceID   int64  `json:"invoice_id"`
	Amount      int64  `json:"amount"`
	PaidOn      string `json:"paid_on"`
	Method      string `json:"method"`
	ProviderRef string `json:"provider_ref,omitempty"`
}

func paymentToResponse(p core.Payment) paymentResponse {
	return paymentResponse{
		ID: p.ID, InvoiceID: p.InvoiceID, Amount: int64(p.Amount), PaidOn: p.PaidOn.Format(dateLayout),
		Method: p.Method, ProviderRef: p.ProviderRef,
	}
}

// --- helpers ---

// runInTx runs fn in a new transaction on d.Pool, committing on success and
// rolling back (a no-op once committed) otherwise.
func runInTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// pathID parses the {id} path value as an int64. A malformed id is treated
// like an unknown one: the caller should respond 404, not reveal that the
// path shape mattered.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// writeCoreError maps a core package error to an HTTP response.
func writeCoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, core.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, core.ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid state")
	default:
		slog.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// --- contacts handlers ---

func createContact(w http.ResponseWriter, r *http.Request, d Deps) {
	var body contactRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}
	switch body.Kind {
	case "tenant", "client", "supplier":
	default:
		writeError(w, http.StatusUnprocessableEntity, "kind must be tenant, client or supplier")
		return
	}

	p := auth.From(r.Context())
	var created core.Contact
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = core.CreateContact(r.Context(), tx, p.OrgID, core.Contact{
			Kind: body.Kind, Name: body.Name, Email: body.Email, Phone: body.Phone,
			CompanyName: body.CompanyName, ABN: body.ABN, ProviderCustomerRef: body.ProviderCustomerRef,
		})
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "contact", created.ID, "create", body)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, contactToResponse(created))
}

func listContacts(w http.ResponseWriter, r *http.Request, d Deps) {
	p := auth.From(r.Context())
	contacts, err := core.ListContacts(r.Context(), d.Pool, p.OrgID, r.URL.Query().Get("kind"))
	if err != nil {
		slog.Error("list contacts failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := make([]contactResponse, len(contacts))
	for i, c := range contacts {
		resp[i] = contactToResponse(c)
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- invoice handlers ---

func createInvoice(w http.ResponseWriter, r *http.Request, d Deps, getenv func(string) string) {
	var body invoiceRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch body.Source {
	case "rent", "bond", "timesheet", "manual":
	default:
		writeError(w, http.StatusUnprocessableEntity, "source must be rent, bond, timesheet or manual")
		return
	}

	in := core.NewInvoice{
		ContactID: body.ContactID,
		Source:    body.Source,
		DueInDays: body.DueInDays,
		Lines:     make([]core.NewLine, len(body.Lines)),
	}
	for i, l := range body.Lines {
		in.Lines[i] = core.NewLine{
			Description: l.Description, QuantityMilli: l.QuantityMilli,
			UnitAmount: core.Cents(l.UnitAmount), GSTCode: l.GSTCode,
		}
	}

	p := auth.From(r.Context())
	var inv core.Invoice
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		inv, err = core.CreateDraftInvoice(r.Context(), tx, p.OrgID, in)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "invoice", inv.ID, "create_draft", body)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	resp, err := invoiceToResponse(r.Context(), d.Pool, getenv, inv)
	if err != nil {
		slog.Error("building invoice response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func getInvoice(w http.ResponseWriter, r *http.Request, d Deps, getenv func(string) string) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	inv, err := core.GetInvoice(r.Context(), d.Pool, p.OrgID, id)
	if err != nil {
		writeCoreError(w, err)
		return
	}

	resp, err := invoiceToResponse(r.Context(), d.Pool, getenv, inv)
	if err != nil {
		slog.Error("building invoice response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func listInvoices(w http.ResponseWriter, r *http.Request, d Deps, getenv func(string) string) {
	p := auth.From(r.Context())
	invoices, err := core.ListInvoices(r.Context(), d.Pool, p.OrgID, r.URL.Query().Get("status"))
	if err != nil {
		slog.Error("list invoices failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := make([]invoiceResponse, len(invoices))
	for i, inv := range invoices {
		ir, err := invoiceToResponse(r.Context(), d.Pool, getenv, inv)
		if err != nil {
			slog.Error("building invoice response failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		resp[i] = ir
	}
	writeJSON(w, http.StatusOK, resp)
}

// minPayOptionsForDeposit is the QLD rule: a rent or bond invoice needs at
// least this many ways to pay before it can be issued.
const minPayOptionsForDeposit = 2

func issueInvoice(w http.ResponseWriter, r *http.Request, d Deps, getenv func(string) string) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	var inv core.Invoice
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		existing, err := core.GetInvoice(r.Context(), tx, p.OrgID, id)
		if err != nil {
			return err
		}

		if existing.Source == "rent" || existing.Source == "bond" {
			opts, err := payOptionsFor(r.Context(), tx, getenv, p.OrgID)
			if err != nil {
				return err
			}
			if len(opts) < minPayOptionsForDeposit {
				return fmt.Errorf("%w: %s invoices need at least %d pay options", core.ErrInvalidInput, existing.Source, minPayOptionsForDeposit)
			}
		}

		inv, err = core.IssueInvoice(r.Context(), tx, p.OrgID, id, time.Now())
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "invoice", inv.ID, "issue", map[string]any{"number": inv.Number})
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	resp, err := invoiceToResponse(r.Context(), d.Pool, getenv, inv)
	if err != nil {
		slog.Error("building invoice response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func voidInvoice(w http.ResponseWriter, r *http.Request, d Deps, getenv func(string) string) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	var inv core.Invoice
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		inv, err = core.VoidInvoice(r.Context(), tx, p.OrgID, id)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "invoice", inv.ID, "void", nil)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	resp, err := invoiceToResponse(r.Context(), d.Pool, getenv, inv)
	if err != nil {
		slog.Error("building invoice response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func recordPayment(w http.ResponseWriter, r *http.Request, d Deps) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	var body paymentRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch body.Method {
	case "bank_transfer", "payid", "cash":
	default:
		writeError(w, http.StatusUnprocessableEntity, "method must be bank_transfer, payid or cash")
		return
	}
	if body.Amount <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "amount must be positive")
		return
	}
	paidOn, err := time.Parse(dateLayout, body.PaidOn)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "paid_on must be an RFC 3339 date (YYYY-MM-DD)")
		return
	}

	p := auth.From(r.Context())
	var payment core.Payment
	var created bool
	err = runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		payment, created, err = core.RecordPayment(r.Context(), tx, p.OrgID, core.NewPayment{
			InvoiceID: id, Amount: core.Cents(body.Amount), PaidOn: paidOn, Method: body.Method, ProviderRef: body.ProviderRef,
		})
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "payment", payment.ID, "record", body)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, paymentToResponse(payment))
}
