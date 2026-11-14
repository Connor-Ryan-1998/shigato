import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import { setPage } from '../../../../lib/test/app.svelte';
import { mockApi } from '../../../../lib/test/mock-api';
import { signIn } from '../../../../lib/test/session';
import { session } from '../../../../lib/session.svelte';
import Page from './+page.svelte';

const summary = {
	property_id: 3, name: 'Cottage', gross_rent: 2600000, other_income: 0,
	deductions: { council_rates: 240000, sundry: 3000, capital_works: 750000 }, total_deductions: 993000, net: 1607000,
	warnings: ['Category "Travel" has no Item 21 label; counted as Sundry']
};

beforeEach(() => {
	signIn();
	session.setModules(['property', 'reconciliation']);
});

describe('rental report view', () => {
	it('lays out the Item 21 schedule with warnings first', async () => {
		setPage({ params: { org: 'acme' }, url: '/acme/reports/rental?kind=item21&fy=2027&property_id=3' });
		mockApi({ 'GET /api/v1/orgs/acme/rental-reports/item21?fy=2027&property_id=3': { fy: 2027, properties: [summary] } });
		render(Page);
		expect(await screen.findByRole('heading', { name: 'Item 21 rental schedule FY2026–27' })).toBeTruthy();
		expect(screen.getByText('Category "Travel" has no Item 21 label; counted as Sundry')).toBeTruthy();
		const table = screen.getByRole('table', { name: 'Cottage' });
		const row = within(table).getByRole('row', { name: /Council rates/ });
		expect(within(row).getByText('$2,400.00')).toBeTruthy();
		expect(within(table).getByRole('row', { name: /Capital works deductions/ })).toBeTruthy();
		expect(within(table).getByRole('row', { name: /Net rent/ }).textContent).toContain('$16,070.00');
		expect(screen.getByRole('button', { name: 'Print' })).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Download CSV' }).getAttribute('href')).toBe('/api/v1/orgs/acme/rental-reports/item21?fy=2027&property_id=3&format=csv');
	});

	it('lists the accountant detail sections', async () => {
		setPage({ params: { org: 'acme' }, url: '/acme/reports/rental?kind=accountant&fy=2027' });
		mockApi({ 'GET /api/v1/orgs/acme/rental-reports/accountant?fy=2027': { fy: 2027, properties: [{
			...summary, warnings: [],
			income: [{ paid_on: '2026-07-03', invoice_id: 1, invoice_number: 42, category: 'Rent', incl_gst: 50000, gst: 0, ex_gst: 50000 }],
			expenses: [{ expense_id: 5, line_id: 6, spent_on: '2026-08-01', supplier: 'BCC', description: 'Rates', category: 'Council rates', capital: false, item21_label: 'council_rates', incl_gst: 240000, gst: 0, ex_gst: 240000, receipt_file_id: null, receipt_name: '' }],
			capital: [{ expense_id: 7, line_id: 8, spent_on: '2026-08-10', supplier: '', description: 'Aircon', category: 'Equipment', capital: true, item21_label: null, incl_gst: 320000, gst: 29091, ex_gst: 290909, receipt_file_id: null, receipt_name: '' }],
			depreciation: [{ item_id: 1, name: 'Building', type: 'capital_works', amount: 750000 }]
		}] } });
		render(Page);
		expect(await screen.findByRole('heading', { name: 'Accountant report FY2026–27' })).toBeTruthy();
		expect(screen.getByRole('table', { name: 'Cottage income' }).textContent).toContain('Invoice #42');
		expect(screen.getByRole('table', { name: 'Cottage expenses' }).textContent).toContain('Rates');
		expect(screen.getByRole('table', { name: 'Cottage capital purchases' }).textContent).toContain('Aircon');
		expect(screen.getByRole('table', { name: 'Cottage depreciation' }).textContent).toContain('$7,500.00');
	});
});
