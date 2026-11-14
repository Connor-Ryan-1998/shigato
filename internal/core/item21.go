package core

import "fmt"

// Item21Label is a label on the ATO individual tax return's rental
// schedule (Item 21). Expense categories map to one; depreciation items
// feed CapitalAllowances or CapitalWorks.
type Item21Label string

const (
	Item21Advertising       Item21Label = "advertising"
	Item21BodyCorporate     Item21Label = "body_corporate"
	Item21Borrowing         Item21Label = "borrowing"
	Item21Cleaning          Item21Label = "cleaning"
	Item21CouncilRates      Item21Label = "council_rates"
	Item21CapitalAllowances Item21Label = "capital_allowances"
	Item21Gardening         Item21Label = "gardening"
	Item21Insurance         Item21Label = "insurance"
	Item21Interest          Item21Label = "interest"
	Item21LandTax           Item21Label = "land_tax"
	Item21Legal             Item21Label = "legal"
	Item21PestControl       Item21Label = "pest_control"
	Item21AgentFees         Item21Label = "agent_fees"
	Item21Repairs           Item21Label = "repairs"
	Item21CapitalWorks      Item21Label = "capital_works"
	Item21Stationery        Item21Label = "stationery"
	Item21Water             Item21Label = "water"
	Item21Sundry            Item21Label = "sundry"
)

// Item21Labels lists every label in rental-schedule display order.
var Item21Labels = []Item21Label{
	Item21Advertising, Item21BodyCorporate, Item21Borrowing, Item21Cleaning,
	Item21CouncilRates, Item21CapitalAllowances, Item21Gardening, Item21Insurance,
	Item21Interest, Item21LandTax, Item21Legal, Item21PestControl, Item21AgentFees,
	Item21Repairs, Item21CapitalWorks, Item21Stationery, Item21Water, Item21Sundry,
}

var item21Titles = map[Item21Label]string{
	Item21Advertising:       "Advertising for tenants",
	Item21BodyCorporate:     "Body corporate fees and charges",
	Item21Borrowing:         "Borrowing expenses",
	Item21Cleaning:          "Cleaning",
	Item21CouncilRates:      "Council rates",
	Item21CapitalAllowances: "Capital allowances (depreciation on plant)",
	Item21Gardening:         "Gardening/lawn mowing",
	Item21Insurance:         "Insurance",
	Item21Interest:          "Interest on loans",
	Item21LandTax:           "Land tax",
	Item21Legal:             "Legal fees",
	Item21PestControl:       "Pest control",
	Item21AgentFees:         "Property agent fees/commission",
	Item21Repairs:           "Repairs and maintenance",
	Item21CapitalWorks:      "Capital works deductions",
	Item21Stationery:        "Stationery, telephone and postage",
	Item21Water:             "Water charges",
	Item21Sundry:            "Sundry rental expenses",
}

// Item21Title returns l's rental-schedule wording.
func Item21Title(l Item21Label) string { return item21Titles[l] }

// DepreciationOnly reports whether l is fed only by depreciation items, so
// an expense category may not carry it.
func (l Item21Label) DepreciationOnly() bool {
	return l == Item21CapitalAllowances || l == Item21CapitalWorks
}

// ParseItem21Label validates s as an Item21Label.
func ParseItem21Label(s string) (Item21Label, error) {
	if _, ok := item21Titles[Item21Label(s)]; ok {
		return Item21Label(s), nil
	}
	return "", fmt.Errorf("%w: unknown item 21 label %q", ErrInvalidInput, s)
}
