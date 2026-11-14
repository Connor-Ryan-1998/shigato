package property

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/module"
	"shigoto/internal/reports"
)

// registerRentalRoutes registers the bills view and the rental tax
// reports. Rental reports need reports_read, like every other report.
func registerRentalRoutes(mux *http.ServeMux, d module.Deps) {
	mux.Handle("GET /api/v1/orgs/{org}/properties/{id}/bills",
		d.Auth.RequireOrg(auth.PermPropertyRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			billsHandler(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/rental-reports/{kind}",
		d.Auth.RequireOrg(auth.PermReportsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rentalReportHandler(w, r, d)
		})))
}

// parseFY reads the required ?fy=<end year> parameter, writing a 422 and
// returning false when it's missing or malformed.
func parseFY(w http.ResponseWriter, r *http.Request) (int, bool) {
	fy, err := strconv.Atoi(r.URL.Query().Get("fy"))
	if err != nil || fy < 1900 || fy > 2200 {
		module.WriteError(w, http.StatusUnprocessableEntity, "fy must be a financial year's end year, e.g. 2027")
		return 0, false
	}
	return fy, true
}

type billLineJSON struct {
	ExpenseID     int64             `json:"expense_id"`
	LineID        int64             `json:"line_id"`
	PropertyID    int64             `json:"property_id"`
	SpentOn       string            `json:"spent_on"`
	Supplier      string            `json:"supplier"`
	Description   string            `json:"description"`
	Category      string            `json:"category"`
	Capital       bool              `json:"capital"`
	Item21Label   *core.Item21Label `json:"item21_label"`
	GSTCode       string            `json:"gst_code"`
	InclGST       int64             `json:"incl_gst"`
	GST           int64             `json:"gst"`
	ExGST         int64             `json:"ex_gst"`
	ReceiptFileID *int64            `json:"receipt_file_id"`
	ReceiptName   string            `json:"receipt_name"`
}

func billToJSON(b BillLine) billLineJSON {
	return billLineJSON{
		ExpenseID: b.ExpenseID, LineID: b.LineID, PropertyID: b.PropertyID, SpentOn: b.SpentOn.Format(dateLayout),
		Supplier: b.Supplier, Description: b.Description, Category: b.Category, Capital: b.Capital,
		Item21Label: b.Label, GSTCode: string(b.Code), InclGST: int64(b.Incl), GST: int64(b.GST), ExGST: int64(b.Ex),
		ReceiptFileID: b.ReceiptFileID, ReceiptName: b.ReceiptName,
	}
}

func billsToJSON(bills []BillLine) []billLineJSON {
	out := make([]billLineJSON, len(bills))
	for i, b := range bills {
		out[i] = billToJSON(b)
	}
	return out
}

func billsHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	id, ok := module.PathID(r)
	if !ok {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	fy, ok := parseFY(w, r)
	if !ok {
		return
	}
	orgID := auth.From(r.Context()).OrgID
	if err := requireProperty(r.Context(), d.Pool, orgID, id); err != nil {
		module.WriteCoreError(w, err)
		return
	}
	month, err := reports.OrgFYStartMonth(r.Context(), d.Pool, orgID)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	from, to, err := reports.FinancialYear(fy, month)
	if err != nil {
		module.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	bills, err := Bills(r.Context(), d.Pool, orgID, &id, from, to)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}
	totals := map[string]int64{}
	for k, v := range categoryTotals(bills) {
		totals[k] = int64(v)
	}
	module.WriteJSON(w, http.StatusOK, map[string]any{
		"fy": fy, "from": from.Format(dateLayout), "to": to.Format(dateLayout),
		"lines": billsToJSON(bills), "totals": totals,
	})
}

type incomeJSON struct {
	PaidOn        string `json:"paid_on"`
	InvoiceID     int64  `json:"invoice_id"`
	InvoiceNumber *int   `json:"invoice_number"`
	Category      string `json:"category"`
	InclGST       int64  `json:"incl_gst"`
	GST           int64  `json:"gst"`
	ExGST         int64  `json:"ex_gst"`
}

type depreciationLineJSON struct {
	ItemID int64  `json:"item_id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Amount int64  `json:"amount"`
}

type item21PropertyJSON struct {
	PropertyID      int64            `json:"property_id"`
	Name            string           `json:"name"`
	GrossRent       int64            `json:"gross_rent"`
	OtherIncome     int64            `json:"other_income"`
	Deductions      map[string]int64 `json:"deductions"`
	TotalDeductions int64            `json:"total_deductions"`
	Net             int64            `json:"net"`
	Warnings        []string         `json:"warnings"`
}

type accountantPropertyJSON struct {
	item21PropertyJSON
	Income       []incomeJSON           `json:"income"`
	Expenses     []billLineJSON         `json:"expenses"`
	Capital      []billLineJSON         `json:"capital"`
	Depreciation []depreciationLineJSON `json:"depreciation"`
}

func item21JSON(rp rentalProperty, res Item21Result) item21PropertyJSON {
	out := item21PropertyJSON{
		PropertyID: rp.Property.ID, Name: rp.Property.Name,
		GrossRent: int64(res.GrossRent), OtherIncome: int64(res.OtherIncome),
		Deductions: map[string]int64{}, TotalDeductions: int64(res.TotalDeductions()),
		Warnings: res.Warnings,
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for l, v := range res.Deductions {
		out.Deductions[string(l)] = int64(v)
	}
	out.Net = out.GrossRent + out.OtherIncome - out.TotalDeductions
	return out
}

func accountantJSON(rp rentalProperty, res Item21Result, fy int) accountantPropertyJSON {
	out := accountantPropertyJSON{item21PropertyJSON: item21JSON(rp, res),
		Income: []incomeJSON{}, Expenses: []billLineJSON{}, Capital: []billLineJSON{}, Depreciation: []depreciationLineJSON{}}
	for _, row := range rp.Income {
		out.Income = append(out.Income, incomeJSON{
			PaidOn: row.PaidOn.Format(dateLayout), InvoiceID: row.InvoiceID, InvoiceNumber: row.InvoiceNumber,
			Category: row.Category, InclGST: int64(row.Incl), GST: int64(row.GST), ExGST: int64(row.Ex),
		})
	}
	for _, b := range rp.Bills {
		if b.Capital {
			out.Capital = append(out.Capital, billToJSON(b))
		} else {
			out.Expenses = append(out.Expenses, billToJSON(b))
		}
	}
	for _, item := range rp.Depreciation {
		if amount := item.AmountFor(fy); amount != 0 {
			out.Depreciation = append(out.Depreciation, depreciationLineJSON{ItemID: item.ID, Name: item.Name, Type: item.Type, Amount: int64(amount)})
		}
	}
	return out
}

func rentalReportHandler(w http.ResponseWriter, r *http.Request, d module.Deps) {
	kind := r.PathValue("kind")
	if kind != "accountant" && kind != "item21" {
		module.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	fy, ok := parseFY(w, r)
	if !ok {
		return
	}
	var propertyID *int64
	if s := r.URL.Query().Get("property_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			module.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		propertyID = &id
	}
	orgID := auth.From(r.Context()).OrgID
	props, from, to, err := loadRental(r.Context(), d.Pool, orgID, propertyID, fy)
	if err != nil {
		module.WriteCoreError(w, err)
		return
	}

	if r.URL.Query().Get("format") == "csv" {
		writeRentalCSV(w, kind, fy, props)
		return
	}
	var list []any
	for _, rp := range props {
		res := Item21(rp.Income, rp.Bills, rp.Depreciation, fy)
		if kind == "item21" {
			list = append(list, item21JSON(rp, res))
		} else {
			list = append(list, accountantJSON(rp, res, fy))
		}
	}
	if list == nil {
		list = []any{}
	}
	module.WriteJSON(w, http.StatusOK, map[string]any{
		"fy": fy, "from": from.Format(dateLayout), "to": to.Format(dateLayout), "properties": list,
	})
}

func writeRentalCSV(w http.ResponseWriter, kind string, fy int, props []rentalProperty) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="rental-%s-FY%d.csv"`, kind, fy))
	cw := csv.NewWriter(w)
	if kind == "item21" {
		_ = cw.Write([]string{"property", "label", "amount"})
		for _, rp := range props {
			res := Item21(rp.Income, rp.Bills, rp.Depreciation, fy)
			name := rp.Property.Name
			_ = cw.Write([]string{name, "Gross rent", formatDollars(res.GrossRent)})
			_ = cw.Write([]string{name, "Other rental related income", formatDollars(res.OtherIncome)})
			for _, l := range orderedDeductions(res) {
				_ = cw.Write([]string{name, core.Item21Title(l), formatDollars(res.Deductions[l])})
			}
		}
		cw.Flush()
		return
	}

	_ = cw.Write([]string{"property", "section", "date", "category", "item21_label", "description", "supplier", "incl_gst", "gst", "ex_gst", "receipt"})
	for _, rp := range props {
		name := rp.Property.Name
		for _, row := range rp.Income {
			desc := fmt.Sprintf("Invoice %d", row.InvoiceID)
			if row.InvoiceNumber != nil {
				desc = fmt.Sprintf("Invoice #%d", *row.InvoiceNumber)
			}
			_ = cw.Write([]string{name, "income", row.PaidOn.Format(dateLayout), row.Category, "", desc, "",
				formatDollars(row.Incl), formatDollars(row.GST), formatDollars(row.Ex), ""})
		}
		for _, b := range rp.Bills {
			section, label := "expense", ""
			if b.Capital {
				section = "capital"
			} else if b.Label != nil {
				label = string(*b.Label)
			} else {
				label = string(core.Item21Sundry)
			}
			_ = cw.Write([]string{name, section, b.SpentOn.Format(dateLayout), b.Category, label, b.Description, b.Supplier,
				formatDollars(b.Incl), formatDollars(b.GST), formatDollars(b.Ex), b.ReceiptName})
		}
		for _, item := range rp.Depreciation {
			amount := item.AmountFor(fy)
			if amount == 0 {
				continue
			}
			label := core.Item21CapitalAllowances
			if item.Type == "capital_works" {
				label = core.Item21CapitalWorks
			}
			_ = cw.Write([]string{name, "depreciation", item.InstalledOn.Format(dateLayout), item.Type, string(label), item.Name, "",
				formatDollars(amount), "0.00", formatDollars(amount), ""})
		}
	}
	cw.Flush()
}
