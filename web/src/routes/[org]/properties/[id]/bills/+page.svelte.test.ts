import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { goto, setPage } from '../../../../../lib/test/app.svelte';
import { apiMock, mockApi } from '../../../../../lib/test/mock-api';
import { signIn } from '../../../../../lib/test/session';
import { session } from '../../../../../lib/session.svelte';
import Page from './+page.svelte';

const base = '/api/v1/orgs/acme/properties/1/bills';
const line = (over: object) => ({
	expense_id: 11, line_id: 21, property_id: 1, spent_on: '2026-08-01', supplier: 'Brisbane City Council',
	description: 'Q1 rates', category: 'Council rates', capital: false, item21_label: 'council_rates', gst_code: 'N-T',
	incl_gst: 80000, gst: 0, ex_gst: 80000, receipt_file_id: 9, receipt_name: 'rates.pdf', ...over
});
const bills = {
	fy: 2027, from: '2026-07-01', to: '2027-06-30',
	lines: [line({}), line({ expense_id: 12, line_id: 22, category: 'Equipment', capital: true, description: 'Aircon', receipt_file_id: null, receipt_name: '' })],
	totals: { 'Council rates': 80000, Equipment: 320000 }
};

beforeEach(() => {
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(new Date('2026-11-14T10:00:00'));
	setPage({ params: { org: 'acme', id: '1' }, url: '/acme/properties/1/bills' });
	signIn();
	session.setModules(['property', 'reconciliation']);
	mockApi({ [`GET ${base}?fy=2027`]: bills, [`GET ${base}?fy=2026`]: { ...bills, fy: 2026, lines: [], totals: {} } });
});

describe('property bills tab', () => {
	it('lists the current financial year with totals and receipts', async () => {
		render(Page);
		const table = await screen.findByRole('table', { name: 'Bills FY2026–27' });
		expect(within(table).getByText('Q1 rates')).toBeTruthy();
		expect(within(table).getByRole('link', { name: 'rates.pdf' }).getAttribute('href')).toBe('/api/v1/orgs/acme/files/9');
		expect(within(table).getByRole('link', { name: 'View expense 11' }).getAttribute('href')).toBe('/acme/expenses/11');
		expect(within(table).getByText('Capital')).toBeTruthy();
		const totals = screen.getByRole('table', { name: 'Totals by category' });
		expect(within(totals).getByText('$800.00')).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Add bill' }).getAttribute('href')).toBe('/acme/expenses/new?property_id=1');
	});

	it('changes financial year through the URL', async () => {
		render(Page);
		await screen.findByRole('table', { name: 'Bills FY2026–27' });
		await fireEvent.change(screen.getByLabelText('Financial year'), { target: { value: '2026' } });
		expect(goto).toHaveBeenCalledWith('?fy=2026', expect.objectContaining({ replaceState: true }));
		setPage({ url: '/acme/properties/1/bills?fy=2026' });
		expect(await screen.findByText('No bills recorded for FY2025–26.')).toBeTruthy();
		expect(apiMock).toHaveBeenCalledWith('GET', `${base}?fy=2026`);
	});

	it('hides Add bill without the reconciliation module', async () => {
		session.setModules(['property']);
		render(Page);
		await screen.findByRole('table', { name: 'Bills FY2026–27' });
		expect(screen.queryByRole('link', { name: 'Add bill' })).toBeNull();
	});

	it('hides receipt links without report access', async () => {
		signIn({ role: 'staff', perms: ['property_read'] });
		render(Page);
		await screen.findByRole('table', { name: 'Bills FY2026–27' });
		expect(screen.queryByRole('link', { name: 'rates.pdf' })).toBeNull();
	});
});
