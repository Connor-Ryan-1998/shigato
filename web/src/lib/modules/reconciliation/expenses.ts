export type ExpenseLine = {
	id: number;
	category_id: number;
	amount: number;
	gst_code: string;
	gst_amount: number;
	property_id?: number;
	project_id?: number;
};

export type Expense = {
	id: number;
	spent_on: string;
	description: string;
	supplier_contact_id?: number;
	receipt_file_id?: number;
	bank_line_id?: number;
	total: number;
	lines: ExpenseLine[];
};
