// API shapes for the property page (internal/property/routes*.go).

export type Tenancy = {
	lease_id: number;
	rent: number;
	frequency: string;
	primary_tenant_id: number;
	primary_tenant_name: string;
};

export type PropertyDetail = {
	id: number;
	name: string;
	address?: string;
	kind?: string;
	residential: boolean;
	contract_date: string | null;
	settlement_date: string | null;
	purchase_price: number | null;
	stamp_duty: number | null;
	purchase_legal_costs: number | null;
	year_built: number | null;
	lot_plan: string;
	land_area_m2: number | null;
	council_account_ref: string;
	water_account_ref: string;
	notes: string;
	current_tenancy: Tenancy | null;
};

export type InsuranceType = 'building' | 'landlord' | 'strata_contents' | 'other';

export const INSURANCE_TYPES: { value: InsuranceType; label: string }[] = [
	{ value: 'landlord', label: 'Landlord' },
	{ value: 'building', label: 'Building' },
	{ value: 'strata_contents', label: 'Strata contents' },
	{ value: 'other', label: 'Other' }
];

export type InsurancePolicy = {
	id: number;
	property_id: number;
	insurer: string;
	type: InsuranceType;
	policy_number: string;
	start_date: string;
	renewal_date: string;
	sum_insured: number | null;
	premium: number | null;
	excess: number | null;
	file_id: number | null;
	file_name: string | null;
};

export type DepreciationType = 'capital_works' | 'plant_equipment';

export const DEPRECIATION_TYPES: { value: DepreciationType; label: string }[] = [
	{ value: 'capital_works', label: 'Capital works' },
	{ value: 'plant_equipment', label: 'Plant & equipment' }
];

export type DepreciationItem = {
	id: number;
	property_id: number;
	name: string;
	type: DepreciationType;
	installed_on: string;
	cost: number | null;
	notes: string;
	expense_id: number | null;
	amounts: { fy: number; amount: number }[];
	documents: { id: number; original_name: string }[];
};

export type UploadedFile = { id: number; original_name: string };

// Item 21 labels in rental-schedule order (internal/core/item21.go).
export const ITEM21_LABELS: { value: string; label: string; depreciationOnly?: boolean }[] = [
	{ value: 'advertising', label: 'Advertising for tenants' },
	{ value: 'body_corporate', label: 'Body corporate fees and charges' },
	{ value: 'borrowing', label: 'Borrowing expenses' },
	{ value: 'cleaning', label: 'Cleaning' },
	{ value: 'council_rates', label: 'Council rates' },
	{ value: 'capital_allowances', label: 'Capital allowances (depreciation on plant)', depreciationOnly: true },
	{ value: 'gardening', label: 'Gardening/lawn mowing' },
	{ value: 'insurance', label: 'Insurance' },
	{ value: 'interest', label: 'Interest on loans' },
	{ value: 'land_tax', label: 'Land tax' },
	{ value: 'legal', label: 'Legal fees' },
	{ value: 'pest_control', label: 'Pest control' },
	{ value: 'agent_fees', label: 'Property agent fees/commission' },
	{ value: 'repairs', label: 'Repairs and maintenance' },
	{ value: 'capital_works', label: 'Capital works deductions', depreciationOnly: true },
	{ value: 'stationery', label: 'Stationery, telephone and postage' },
	{ value: 'water', label: 'Water charges' },
	{ value: 'sundry', label: 'Sundry rental expenses' }
];

export const item21Title = (value: string) => ITEM21_LABELS.find((l) => l.value === value)?.label ?? value;

// centsInput renders optional cents as an editable dollar string.
export const centsInput = (c: number | null | undefined) => (c === null || c === undefined ? '' : (c / 100).toFixed(2));
