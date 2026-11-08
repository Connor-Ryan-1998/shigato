import { beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../../lib/session.svelte';
const page = $state({ params: { org: 'acme', id: '7' } });
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../../lib/api', () => ({ api: apiMock, ApiError: class ApiError extends Error {} }));
import Page from './+page.svelte';
const expense = { id: 7, spent_on: '2026-11-01', description: 'Office supplies', supplier_contact_id: 4, receipt_file_id: 9, bank_line_id: 12, total: 11000, lines: [{ id: 1, category_id: 2, amount: 11000, gst_code: 'GST', gst_amount: 1000, property_id: 3 }] };
function route(_m: string,p: string): Promise<unknown> {
	if (p.endsWith('/categories')) return Promise.resolve([{ id: 2, name: 'Supplies', type: 'expense' }]);
	if (p.endsWith('/contacts?kind=supplier')) return Promise.resolve([{ id: 4, name: 'Shop' }]);
	if (p.endsWith('/properties')) return Promise.resolve([{ id: 3, name: 'Cottage' }]);
	return Promise.resolve(expense);
}
beforeEach(() => {
	page.params = { org: 'acme', id: '7' };
	session.setMe({ user: { id: 1, email: 'book@example.com', name: 'Bookkeeper' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'bookkeeper', perms: ['reports_read', 'invoices_read'] })) });
	session.setOrg('acme'); session.setModules(['reconciliation', 'property']);
	apiMock.mockReset(); apiMock.mockImplementation(route);
});
describe('Expense details', () => {
	it('shows resolved splits, server GST, supplier, bank status and receipt download', async () => {
		render(Page);
		const receipt = await screen.findByRole('link', { name: 'Download receipt' });
		expect(receipt.getAttribute('href')).toBe('/api/v1/orgs/acme/files/9');
		await screen.findByText('Supplies'); await screen.findByText('Shop'); await screen.findByText('Cottage');
		expect(screen.getByText('$10.00')).toBeTruthy();
		expect(screen.getByText('Matched to bank line 12')).toBeTruthy();
		expect(screen.queryByRole('button', { name: /Save|Delete|Edit/ })).toBeNull();
	});
	it('handles optional fields and failed lookups without hiding stored details', async () => {
		apiMock.mockImplementation((_m: string,p: string) => p.includes('/expenses/') ? Promise.resolve({ ...expense, supplier_contact_id: undefined, receipt_file_id: undefined, bank_line_id: undefined }) : Promise.reject(new Error('Lookup offline')));
		render(Page);
		await screen.findByText('No receipt attached.');
		await screen.findByText('Category #2');
		expect(screen.getByText('Not matched to bank')).toBeTruthy();
	});
	it('reports missing/failed detail fetch and supports retry', async () => {
		apiMock.mockRejectedValueOnce(new Error('offline')).mockImplementation(route);
		render(Page);
		await screen.findByText('Could not load expense.');
		expect(screen.queryByRole('link', { name: 'Download receipt' })).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'Retry expense' }));
		await screen.findByRole('link', { name: 'Download receipt' });
	});
	it('drops old expense/receipt immediately across org and id changes', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => p.endsWith('/expenses/7') ? new Promise((resolve) => { finish = resolve; }) : p.endsWith('/expenses/8') ? Promise.resolve({ ...expense, id: 8, description: 'Other expense', receipt_file_id: 10 }) : route(m,p));
		render(Page);
		page.params = { org: 'other', id: '8' }; session.setOrg('other'); flushSync();
		await screen.findByText('Other expense');
		finish(expense); await Promise.resolve(); flushSync();
		expect(screen.getByRole('link', { name: 'Download receipt' }).getAttribute('href')).toBe('/api/v1/orgs/other/files/10');
		expect(screen.queryByText('Office supplies')).toBeNull();
		page.params.org = 'unknown'; flushSync();
		expect(screen.queryByRole('link', { name: 'Download receipt' })).toBeNull();
	});
});
