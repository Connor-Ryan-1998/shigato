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
