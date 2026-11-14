import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../../../lib/test/app.svelte';
import { apiMock } from '../../../../lib/test/mock-api';
import { flushSync } from 'svelte';
import { act, fireEvent, render, screen, within } from '@testing-library/svelte';
import { session } from '../../../../lib/session.svelte';

import { ApiError } from '../../../../lib/api';
import Page from './+page.svelte';

const lease = {
	id: 4, start_date: '2026-01-01', frequency: 'weekly', bond_amount: 0, bond_status: 'due',
	rent_changes: [{ id: 1, effective_date: '2026-01-01', amount: 50000 }], charges: []
};
const affected = [
	{ invoice_id: 9, status: 'draft', start: '2026-01-01', end: '2026-01-07' },
	{ invoice_id: 10, invoice_number: 42, status: 'paid', start: '2026-01-08', end: '2026-01-14' }
];
const ledger = {
	periods: affected.map((p) => ({ ...p, amount_due: 50000, invoice_status: p.status, paid: p.status === 'paid' ? 50000 : 0 })),
	opening_arrears: 0, arrears: 0, invoiced_not_due: 0, unissued: 50000
};
function route(method: string, path: string): Promise<unknown> {
	if (method === 'GET') return Promise.resolve(path.endsWith('/ledger') ? ledger : { ...lease, id: Number(path.split('/').at(-1)) });
	return Promise.resolve({ warnings: ['Existing rent increase warning', 'Existing invoices were not recalculated'], affected_invoices: affected });
}
beforeEach(() => {
	setPage({ params: { org: 'acme', id: '4' } });
	page.params = { org: 'acme', id: '4' };
	session.setMe({ user: { id: 1, name: 'Owner', email: 'owner@example.com' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'owner', perms: ['property_read', 'property_write'] })) });
	session.setOrg('acme');

	apiMock.mockImplementation(route);
});
async function actionForm(button = 'Change rent') {
	await fireEvent.click(await screen.findByRole('button', { name: button }));
	return within(screen.getByRole('dialog')).getByRole('button', { name: button }).closest('form')!;
}

describe('billed-period lease warnings', () => {
	it('opens actions explicitly rather than showing embedded editors', async () => {
		render(Page);
		await screen.findByRole('heading', { name: 'Ledger' });
		expect(screen.queryByLabelText('Rent ($)')).toBeNull();
		await actionForm();
		expect(screen.getByRole('dialog', { name: 'Change rent' })).toBeTruthy();
	});
	it.each(['Change rent', 'Add charge', 'Terminate lease'])('keeps affected invoice links visible after %s save and refresh', async (button) => {
		render(Page);
		await fireEvent.submit(await actionForm(button));
		const panel = await screen.findByRole('status', { name: 'Invoice review required' });
		expect(within(panel).getByRole('link', { name: /Draft invoice 9/ }).getAttribute('href')).toBe('/acme/invoices/9');
		expect(within(panel).getByRole('link', { name: /Invoice #42/ }).getAttribute('href')).toBe('/acme/invoices/10');
		expect(within(panel).getByText(/2026-01-01 to 2026-01-07/)).toBeTruthy();
		expect(await screen.findByText(/Existing rent increase warning/)).toBeTruthy();
		await vi.waitFor(() => expect(apiMock.mock.calls.filter(([m,p]) => m === 'GET' && p.endsWith('/ledger'))).toHaveLength(2));
		expect(screen.getByRole('status', { name: 'Invoice review required' })).toBeTruthy();
	});
	it('does not invent affected invoices for non-overlapping save', async () => {
		apiMock.mockImplementation((m: string,p: string) => m === 'POST' ? Promise.resolve({}) : route(m,p));
		render(Page);
		await fireEvent.submit(await actionForm());
		await screen.findByText('Rent change saved.');
		expect(screen.queryByRole('status', { name: 'Invoice review required' })).toBeNull();
	});
	it('preserves input and validation error on failed save', async () => {
		apiMock.mockImplementation((m: string,p: string) => m === 'POST' ? Promise.reject(new ApiError(422,'invalid rent date')) : route(m,p));
		render(Page);
		const form = await actionForm();
		await fireEvent.input(screen.getByLabelText('Rent ($)'), { target: { value: '520' } });
		await fireEvent.submit(form);
		await screen.findByText('invalid rent date');
		expect((screen.getByLabelText('Rent ($)') as HTMLInputElement).value).toBe('520');
		expect(screen.queryByRole('status', { name: 'Invoice review required' })).toBeNull();
	});
	it('does not apply older lease load after a new lease loads', async () => {
		let finish!: (value: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => p.endsWith('/leases/4') ? new Promise((resolve) => { finish = resolve; }) : route(m,p));
		render(Page);
		page.params.id = '5';
		flushSync();
		await actionForm();
		await act(() => { finish({ ...lease, warnings: ['Old lease warning'] }); });
		expect(screen.queryByText('Old lease warning')).toBeNull();
	});
	it('does not show late mutation warning links under another org or lease', async () => {
		let finish!: (value: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => m === 'POST' ? new Promise((resolve) => { finish = resolve; }) : route(m,p));
		render(Page);
		await fireEvent.submit(await actionForm());
		page.params = { org: 'other', id: '5' };
		session.setOrg('other');
		flushSync();
		await actionForm();
		await act(() => { finish({ warnings: ['Old mutation warning'], affected_invoices: affected }); });
		expect(screen.queryByRole('status', { name: 'Invoice review required' })).toBeNull();
		expect(screen.queryByText(/Old mutation warning/)).toBeNull();
	});
	it('does not submit old lease form to changed route before effects run', async () => {
		render(Page);
		const form = await actionForm();
		page.params.id = '5';
		form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		expect(apiMock.mock.calls.filter(([m]) => m === 'POST')).toHaveLength(0);
		flushSync();
	});
	it('readers see no lease write actions', async () => {
		session.setMe({ user: { id: 1, name: 'Bookkeeper', email: 'book@example.com' }, orgs: [{ slug: 'acme', name: 'Acme', role: 'bookkeeper', perms: ['property_read'] }] });
		render(Page);
		await screen.findByRole('heading', { name: 'Ledger' });
		expect(screen.queryByRole('button', { name: 'Change rent' })).toBeNull();
	});
});
