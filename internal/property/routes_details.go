package property

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
)

// registerDetailRoutes registers the property page's insurance, document
// and depreciation routes.
func registerDetailRoutes(mux *http.ServeMux, d module.Deps) {
	read := func(h func(http.ResponseWriter, *http.Request, module.Deps)) http.Handler {
		return d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h(w, r, d) }))
	}
	write := func(h func(http.ResponseWriter, *http.Request, module.Deps)) http.Handler {
		return d.Auth.RequireOrg(auth.PermPropertyWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h(w, r, d) }))
	}
	mux.Handle("GET /api/v1/orgs/{org}/properties/{id}/insurance", read(listInsuranceHandler))
	mux.Handle("POST /api/v1/orgs/{org}/properties/{id}/insurance", write(createInsuranceHandler))
	mux.Handle("PUT /api/v1/orgs/{org}/properties/{id}/insurance/{policyId}", write(updateInsuranceHandler))
	mux.Handle("DELETE /api/v1/orgs/{org}/properties/{id}/insurance/{policyId}", write(deleteInsuranceHandler))
	mux.Handle("POST /api/v1/orgs/{org}/properties/{id}/files", write(uploadPropertyFileHandler))
	mux.Handle("GET /api/v1/orgs/{org}/properties/{id}/files/{fileId}", read(downloadPropertyFileHandler))
	mux.Handle("GET /api/v1/orgs/{org}/properties/{id}/depreciation", read(listDepreciationHandler))
	mux.Handle("POST /api/v1/orgs/{org}/properties/{id}/depreciation", write(createDepreciationHandler))
	mux.Handle("PUT /api/v1/orgs/{org}/properties/{id}/depreciation/{itemId}", write(updateDepreciationHandler))
	mux.Handle("DELETE /api/v1/orgs/{org}/properties/{id}/depreciation/{itemId}", write(deleteDepreciationHandler))
}

// pathInt64 parses the named path value as a positive int64.
func pathInt64(r *http.Request, name string) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return v, err == nil && v > 0
}

// --- insurance ---

type insuranceRequest struct {
	Insurer      string `json:"insurer"`
	Type         string `json:"type"`
	PolicyNumber string `json:"policy_number"`
	StartDate    string `json:"start_date"`
	RenewalDate  string `json:"renewal_date"`
	SumInsured   *int64 `json:"sum_insured"`
	Premium      *int64 `json:"premium"`
	Excess       *int64 `json:"excess"`
	FileID       *int64 `json:"file_id"`
}

type insuranceResponse struct {
	ID           int64   `json:"id"`
	PropertyID   int64   `json:"property_id"`
	Insurer      string  `json:"insurer"`
	Type         string  `json:"type"`
	PolicyNumber string  `json:"policy_number"`
	StartDate    string  `json:"start_date"`
	RenewalDate  string  `json:"renewal_date"`
	SumInsured   *int64  `json:"sum_insured"`
	Premium      *int64  `json:"premium"`
	Excess       *int64  `json:"excess"`
	FileID       *int64  `json:"file_id"`
	FileName     *string `json:"file_name"`
}

func insuranceToResponse(p InsurancePolicy) insuranceResponse {
	resp := insuranceResponse{
		ID: p.ID, PropertyID: p.PropertyID, Insurer: p.Insurer, Type: p.Type, PolicyNumber: p.PolicyNumber,
		StartDate: p.StartDate.Format(dateLayout), RenewalDate: p.RenewalDate.Format(dateLayout),
		SumInsured: int64Ptr(p.SumInsured), Premium: int64Ptr(p.Premium), Excess: int64Ptr(p.Excess), FileID: p.FileID,
	}
	if p.FileID != nil {
		resp.FileName = &p.FileName
	}
	return resp
}

// readInsurance decodes the request body into a policy on propertyID,
// writing a 400/422 and returning false on failure.
func readInsurance(w http.ResponseWriter, r *http.Request, propertyID int64) (InsurancePolicy, insuranceRequest, bool) {
	var body insuranceRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return InsurancePolicy{}, body, false
	}
	start, err1 := parseDate(body.StartDate)
	renewal, err2 := parseDate(body.RenewalDate)
	if err1 != nil || err2 != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "start_date and renewal_date must be YYYY-MM-DD")
		return InsurancePolicy{}, body, false
	}
	return InsurancePolicy{
		PropertyID: propertyID, Insurer: body.Insurer, Type: body.Type, PolicyNumber: body.PolicyNumber,
		StartDate: start, RenewalDate: renewal,
		SumInsured: centsPtr(body.SumInsured), Premium: centsPtr(body.Premium), Excess: centsPtr(body.Excess), FileID: body.FileID,
	}, body, true
}

func listInsuranceHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	policies, err := ListInsurancePolicies(r.Context(), d.Pool, auth.From(r.Context()).OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]insuranceResponse, len(policies))
	for i, p := range policies {
		resp[i] = insuranceToResponse(p)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func createInsuranceHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	in, body, ok := readInsurance(w, r, id)
	if !ok {
		return
	}
	p := auth.From(r.Context())
	var created InsurancePolicy
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		if created, err = CreateInsurancePolicy(r.Context(), tx, p.OrgID, in); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "insurance_policy", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, insuranceToResponse(created))
}

func updateInsuranceHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok1 := module.PathID(r)
	policyID, ok2 := pathInt64(r, "policyId")
	if !ok1 || !ok2 {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	in, body, ok := readInsurance(w, r, id)
	if !ok {
		return
	}
	in.ID = policyID
	p := auth.From(r.Context())
	var updated InsurancePolicy
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		if updated, err = UpdateInsurancePolicy(r.Context(), tx, p.OrgID, in); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "insurance_policy", policyID, "update", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, insuranceToResponse(updated))
}

func deleteInsuranceHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok1 := module.PathID(r)
	policyID, ok2 := pathInt64(r, "policyId")
	if !ok1 || !ok2 {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		if err := DeleteInsurancePolicy(r.Context(), tx, p.OrgID, id, policyID); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "insurance_policy", policyID, "delete", nil)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- documents ---

func uploadPropertyFileHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if err := requireProperty(r.Context(), d.Pool, auth.From(r.Context()).OrgID, id); err != nil {
		module.WriteCoreError(w, err)
		return
	}
	created, ok := module.HandleUpload(w, r, d, nil)
	if !ok {
		return
	}
	module.WriteJSON(w, http.StatusCreated, module.FileToResponse(created))
}

func downloadPropertyFileHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok1 := module.PathID(r)
	fileID, ok2 := pathInt64(r, "fileId")
	if !ok1 || !ok2 {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	orgID := auth.From(r.Context()).OrgID
	attached, err := FileAttachedToProperty(r.Context(), d.Pool, orgID, id, fileID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	if !attached {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := core.GetFile(r.Context(), d.Pool, orgID, fileID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.ServeFile(w, f, d.Cfg.FilesDir)
}

// --- depreciation ---

type depreciationAmountJSON struct {
	FY     int   `json:"fy"`
	Amount int64 `json:"amount"`
}

type depreciationRequest struct {
	Name        string                   `json:"name"`
	Type        string                   `json:"type"`
	InstalledOn string                   `json:"installed_on"`
	Cost        *int64                   `json:"cost"`
	Notes       string                   `json:"notes"`
	ExpenseID   *int64                   `json:"expense_id"`
	Amounts     []depreciationAmountJSON `json:"amounts"`
	DocumentIDs []int64                  `json:"document_ids"`
}

type documentJSON struct {
	ID           int64  `json:"id"`
	OriginalName string `json:"original_name"`
}

type depreciationResponse struct {
	ID          int64                    `json:"id"`
	PropertyID  int64                    `json:"property_id"`
	Name        string                   `json:"name"`
	Type        string                   `json:"type"`
	InstalledOn string                   `json:"installed_on"`
	Cost        *int64                   `json:"cost"`
	Notes       string                   `json:"notes"`
	ExpenseID   *int64                   `json:"expense_id"`
	Amounts     []depreciationAmountJSON `json:"amounts"`
	Documents   []documentJSON           `json:"documents"`
}

func depreciationToResponse(i DepreciationItem) depreciationResponse {
	resp := depreciationResponse{
		ID: i.ID, PropertyID: i.PropertyID, Name: i.Name, Type: i.Type, InstalledOn: i.InstalledOn.Format(dateLayout),
		Cost: int64Ptr(i.Cost), Notes: i.Notes, ExpenseID: i.ExpenseID,
		Amounts: make([]depreciationAmountJSON, len(i.Amounts)), Documents: make([]documentJSON, len(i.Documents)),
	}
	for n, a := range i.Amounts {
		resp.Amounts[n] = depreciationAmountJSON{FY: a.FY, Amount: int64(a.Amount)}
	}
	for n, f := range i.Documents {
		resp.Documents[n] = documentJSON{ID: f.ID, OriginalName: f.OriginalName}
	}
	return resp
}

// readDepreciation decodes the request body into an item on propertyID,
// writing a 400/422 and returning false on failure.
func readDepreciation(w http.ResponseWriter, r *http.Request, propertyID int64) (DepreciationItem, depreciationRequest, bool) {
	var body depreciationRequest
	if err := module.ReadJSON(r, &body); err != nil {
		module.WriteError(w, http.StatusBadRequest, "invalid request body")
		return DepreciationItem{}, body, false
	}
	installed, err := parseDate(body.InstalledOn)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, "installed_on must be YYYY-MM-DD")
		return DepreciationItem{}, body, false
	}
	in := DepreciationItem{
		PropertyID: propertyID, Name: body.Name, Type: body.Type, InstalledOn: installed,
		Cost: centsPtr(body.Cost), Notes: body.Notes, ExpenseID: body.ExpenseID,
	}
	for _, a := range body.Amounts {
		in.Amounts = append(in.Amounts, DepreciationAmount{FY: a.FY, Amount: core.Cents(a.Amount)})
	}
	for _, id := range body.DocumentIDs {
		in.Documents = append(in.Documents, core.File{ID: id})
	}
	return in, body, true
}

func listDepreciationHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	items, err := ListDepreciationItems(r.Context(), d.Pool, auth.From(r.Context()).OrgID, id)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	resp := make([]depreciationResponse, len(items))
	for i, it := range items {
		resp[i] = depreciationToResponse(it)
	}
	module.WriteJSON(w, http.StatusOK, resp)
}

func createDepreciationHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	in, body, ok := readDepreciation(w, r, id)
	if !ok {
		return
	}
	p := auth.From(r.Context())
	var created DepreciationItem
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		if created, err = CreateDepreciationItem(r.Context(), tx, p.OrgID, in); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "depreciation_item", created.ID, "create", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusCreated, depreciationToResponse(created))
}

func updateDepreciationHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok1 := module.PathID(r)
	itemID, ok2 := pathInt64(r, "itemId")
	if !ok1 || !ok2 {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	in, body, ok := readDepreciation(w, r, id)
	if !ok {
		return
	}
	in.ID = itemID
	p := auth.From(r.Context())
	var updated DepreciationItem
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		if updated, err = UpdateDepreciationItem(r.Context(), tx, p.OrgID, in); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "depreciation_item", itemID, "update", body)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	module.WriteJSON(w, http.StatusOK, depreciationToResponse(updated))
}

func deleteDepreciationHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok1 := module.PathID(r)
	itemID, ok2 := pathInt64(r, "itemId")
	if !ok1 || !ok2 {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	p := auth.From(r.Context())
	err := module.RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		if err := DeleteDepreciationItem(r.Context(), tx, p.OrgID, id, itemID); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "depreciation_item", itemID, "delete", nil)
	})
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
