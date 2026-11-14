import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { setPage } from '../../../../lib/test/app.svelte';
import { apiMock, deferred, mockApi } from '../../../../lib/test/mock-api';
import { signIn } from '../../../../lib/test/session';
import Page from './+page.svelte';

const base = '/api/v1/orgs/acme/properties/1';
const cottage = {
	id: 1, name: 'Cottage', address: '1 Main St, Brisbane QLD', kind: 'house', residential: true,
	contract_date: '2020-02-01', settlement_date: '2020-03-15', purchase_price: 65000000, stamp_duty: null,
	purchase_legal_costs: null, year_built: 1995, lot_plan: '', land_area_m2: 607.5, council_account_ref: '',
	water_account_ref: '', notes: '',
	current_tenancy: { lease_id: 7, rent: 55000, frequency: 'weekly', primary_tenant_id: 3, primary_tenant_name: 'Tina' }
};
const policy = {
	id: 5, property_id: 1, insurer: 'Acme Mutual', type: 'landlord', policy_number: 'LL-1',
	start_date: '2025-12-01', renewal_date: '2026-12-01', sum_insured: 50000000, premium: 120000, excess: null,
	file_id: 9, file_name: 'policy.pdf'
};

beforeEach(() => {
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(new Date('2026-11-14T10:00:00'));
	setPage({ params: { org: 'acme', id: '1' } });
	signIn();
	mockApi({ [`GET ${base}`]: cottage, [`GET ${base}/insurance`]: [policy] });
});

describe('property details page', () => {
	it('shows the current tenancy with a link to the lease', async () => {
		render(Page);
		const tenancy = await screen.findByRole('region', { name: 'Current tenancy' });
		expect(within(tenancy).getByText(/Tina/)).toBeTruthy();
		expect(within(tenancy).getByText(/\$550\.00 weekly/)).toBeTruthy();
		expect(within(tenancy).getByRole('link', { name: 'View lease' }).getAttribute('href')).toBe('/acme/leases/7');
	});

	it('says when there is no active lease', async () => {
		mockApi({ [`GET ${base}`]: { ...cottage, current_tenancy: null }, [`GET ${base}/insurance`]: [] });
		render(Page);
		expect(await screen.findByText('No active lease.')).toBeTruthy();
	});

	it('links the address to a map search', async () => {
		render(Page);
		const link = await screen.findByRole('link', { name: 'Open in Maps' });
		expect(link.getAttribute('href')).toBe('https://www.google.com/maps/search/?api=1&query=1%20Main%20St%2C%20Brisbane%20QLD');
		expect(link.getAttribute('rel')).toContain('noopener');
	});

	it('saves every detail field as a full replacement', async () => {
		mockApi({ [`GET ${base}`]: cottage, [`GET ${base}/insurance`]: [], [`PUT ${base}`]: cottage });
		render(Page);
		await fireEvent.input(await screen.findByLabelText('Stamp duty ($)'), { target: { value: '21000' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save details' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', base, expect.objectContaining({
			name: 'Cottage', purchase_price: 65000000, stamp_duty: 2100000, purchase_legal_costs: null,
			year_built: 1995, land_area_m2: 607.5, contract_date: '2020-02-01'
		})));
		expect(await screen.findByText('Details saved.')).toBeTruthy();
	});

	it('flags the current policy renewing within 30 days', async () => {
		render(Page);
		expect(await screen.findByText('Renews in 17 days')).toBeTruthy();
		expect(screen.getByRole('link', { name: 'policy.pdf' }).getAttribute('href')).toBe(`${base}/files/9`);
	});

	it('is read-only without write permission', async () => {
		signIn({ role: 'bookkeeper', perms: ['property_read', 'reports_read'] });
		render(Page);
		await screen.findByRole('region', { name: 'Current tenancy' });
		expect(screen.queryByRole('button', { name: 'Save details' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Add policy' })).toBeNull();
		expect(screen.getByLabelText('Name').matches(':disabled')).toBe(true);
	});

	it('ignores a stale response after switching property', async () => {
		const slow = deferred();
		mockApi({ [`GET ${base}`]: slow.promise, [`GET ${base}/insurance`]: [],
			'GET /api/v1/orgs/acme/properties/2': { ...cottage, id: 2, name: 'Flat' }, 'GET /api/v1/orgs/acme/properties/2/insurance': [] });
		render(Page);
		setPage({ params: { org: 'acme', id: '2' } });
		expect(((await screen.findByLabelText('Name')) as HTMLInputElement).value).toBe('Flat');
		slow.resolve(cottage);
		await vi.waitFor(() => expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Flat'));
	});
});
