import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../../../lib/test/app.svelte';
import { apiMock } from '../../../../lib/test/mock-api';
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../../lib/session.svelte';

import { ApiError } from '../../../../lib/api';
import Page from './+page.svelte';

const contacts = [{ id: 7, name: 'Tina', kind: 'tenant' }, { id: 8, name: 'Tom', kind: 'tenant' }, { id: 9, name: 'Client', kind: 'client' }];
function route(m: string, p: string): Promise<unknown> {
	if (m === 'POST') return Promise.resolve({ id: 4 });
	if (p.endsWith('/properties')) return Promise.resolve([{ id: 1, name: 'Cottage', residential: true }]);
	if (p.endsWith('/contacts')) return Promise.resolve(contacts);
	return Promise.resolve([]);
}
beforeEach(() => {
	setPage({ params: { org: 'acme' } });
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'owner', perms: ['property_read', 'property_write'] })) });
	session.setOrg('acme');

	apiMock.mockImplementation(route);
});
async function fillRent() { await fireEvent.input(await screen.findByLabelText('Rent ($)'), { target: { value: '500' } }); }
function form() { return screen.getByRole('button', { name: 'Create lease' }).closest('form')!; }

describe('existing lease onboarding', () => {
	it('creates co-tenants with one primary and opening balances', async () => {
		render(Page);
		await fillRent();
		await fireEvent.click(screen.getByLabelText('Co-tenant: Tom'));
		await fireEvent.input(screen.getByLabelText('Paid-to date'), { target: { value: '2026-11-07' } });
		await fireEvent.input(screen.getByLabelText('Opening arrears ($)'), { target: { value: '123.45' } });
		await fireEvent.submit(form());
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/leases', expect.objectContaining({
			tenants: [{ contact_id: 7, primary: true }, { contact_id: 8, primary: false }], paid_to_date: '2026-11-07', opening_arrears: 12345
		})));
		expect(screen.queryByLabelText('Co-tenant: Client')).toBeNull();
	});
	it('primary switching never duplicates selected co-tenant', async () => {
		render(Page);
		await fillRent();
		await fireEvent.click(screen.getByLabelText('Co-tenant: Tom'));
		await fireEvent.change(screen.getByLabelText('Tenant', { exact: true }), { target: { value: '8' } });
		await fireEvent.click(screen.getByLabelText('Co-tenant: Tina'));
		await fireEvent.submit(form());
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/leases', expect.objectContaining({
			tenants: [{ contact_id: 8, primary: true }, { contact_id: 7, primary: false }]
		})));
	});
	it('omits blank opening fields and supports explicit zero arrears', async () => {
		render(Page);
		await fillRent();
		await fireEvent.input(screen.getByLabelText('Opening arrears ($)'), { target: { value: '0' } });
		await fireEvent.submit(form());
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/leases', expect.objectContaining({ paid_to_date: undefined, opening_arrears: 0 })));
	});
	it('denies old form submission after route changes', async () => {
		render(Page);
		await fillRent();
		const oldForm = form();
		page.params.org = 'other';
		oldForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		expect(apiMock.mock.calls.filter(([m]) => m === 'POST')).toHaveLength(0);
		session.setOrg('other');
		flushSync();
	});
	it('readers have no onboarding form', async () => {
		session.setMe({ user: { id: 1, email: 'book@example.com', name: 'Book' }, orgs: [{ slug: 'acme', name: 'Acme', role: 'bookkeeper', perms: ['property_read'] }] });
		render(Page);
		await screen.findByRole('alert');
		expect(screen.queryByRole('button', { name: 'Create lease' })).toBeNull();
	});
});
