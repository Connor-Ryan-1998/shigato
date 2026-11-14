import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { setPage } from '../../../../../lib/test/app.svelte';
import { apiMock, mockApi } from '../../../../../lib/test/mock-api';
import { signIn } from '../../../../../lib/test/session';
import Page from './+page.svelte';

const base = '/api/v1/orgs/acme/properties/1/depreciation';
const items = [
	{ id: 1, property_id: 1, name: 'Building', type: 'capital_works', installed_on: '1995-01-01', cost: null, notes: '', expense_id: null,
		amounts: [{ fy: 2026, amount: 750000 }, { fy: 2027, amount: 750000 }], documents: [{ id: 9, original_name: 'bmt.pdf' }] },
	{ id: 2, property_id: 1, name: 'Aircon', type: 'plant_equipment', installed_on: '2026-08-10', cost: 320000, notes: '', expense_id: null,
		amounts: [{ fy: 2027, amount: 45000 }], documents: [] }
];

beforeEach(() => {
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(new Date('2026-11-14T10:00:00'));
	setPage({ params: { org: 'acme', id: '1' }, url: '/acme/properties/1/depreciation' });
	signIn();
	mockApi({ [`GET ${base}`]: items });
});

describe('property depreciation tab', () => {
	it('totals only the selected financial year by type', async () => {
		render(Page);
		const summary = await screen.findByRole('region', { name: 'FY2026–27 depreciation' });
		expect(within(summary).getByText('$7,500.00')).toBeTruthy();
		expect(within(summary).getByText('$450.00')).toBeTruthy();
		expect(within(summary).getByText('$7,950.00')).toBeTruthy();
		const table = screen.getByRole('table', { name: 'Depreciation items' });
		expect(within(table).getByRole('link', { name: 'bmt.pdf' }).getAttribute('href')).toBe('/api/v1/orgs/acme/properties/1/files/9');
	});

	it('uses last year when the URL asks for it', async () => {
		setPage({ url: '/acme/properties/1/depreciation?fy=2026' });
		render(Page);
		const summary = await screen.findByRole('region', { name: 'FY2025–26 depreciation' });
		expect(within(summary).getByText('$7,500.00', { selector: '.total' })).toBeTruthy();
	});

	it('has no editing for read-only users', async () => {
		signIn({ role: 'bookkeeper', perms: ['property_read'] });
		render(Page);
		await screen.findByRole('table', { name: 'Depreciation items' });
		expect(screen.queryByRole('button', { name: 'New item' })).toBeNull();
		expect(screen.queryByRole('button', { name: /^Edit/ })).toBeNull();
	});

	it('rejects a financial year entered twice without saving', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'New item' }));
		await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Oven' } });
		await fireEvent.input(screen.getByLabelText('Installed on'), { target: { value: '2026-09-01' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Add year' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Add year' }));
		await fireEvent.change(screen.getByLabelText('Year 2'), { target: { value: '2027' } });
		await fireEvent.input(screen.getByLabelText('Amount 1 ($)'), { target: { value: '100' } });
		await fireEvent.input(screen.getByLabelText('Amount 2 ($)'), { target: { value: '100' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save item' }));
		expect(await screen.findByText('Each financial year can appear only once.')).toBeTruthy();
		expect(apiMock).not.toHaveBeenCalledWith('POST', base, expect.anything());
	});

	it('saves yearly amounts in cents', async () => {
		mockApi({ [`GET ${base}`]: items, [`POST ${base}`]: { ...items[1], id: 3 } });
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'New item' }));
		await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Oven' } });
		await fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'plant_equipment' } });
		await fireEvent.input(screen.getByLabelText('Installed on'), { target: { value: '2026-09-01' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Add year' }));
		await fireEvent.input(screen.getByLabelText('Amount 1 ($)'), { target: { value: '123.45' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save item' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('POST', base, {
			name: 'Oven', type: 'plant_equipment', installed_on: '2026-09-01', cost: null, notes: '', expense_id: null,
			amounts: [{ fy: 2027, amount: 12345 }], document_ids: []
		}));
	});
});
