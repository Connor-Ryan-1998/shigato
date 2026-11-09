import { beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../../lib/session.svelte';

const page = $state({ params: { org: 'acme' } });
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('$app/navigation', () => ({ goto: vi.fn(), beforeNavigate: vi.fn() }));
vi.mock('../../../../lib/api', () => ({
	api: apiMock,
	ApiError: class ApiError extends Error { constructor(public status: number, message: string) { super(message); } }
}));
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
	page.params.org = 'acme';
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'owner', perms: ['property_read', 'property_write'] })) });
	session.setOrg('acme');
	apiMock.mockReset();
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
	it('keeps balances and tenants after boundary-date validation error', async () => {
		apiMock.mockImplementation((m: string, p: string) => m === 'POST' ? Promise.reject(new ApiError(422, 'paid_to_date must be the last day of a rent period')) : route(m, p));
		render(Page);
		await fillRent();
		await fireEvent.input(screen.getByLabelText('Paid-to date'), { target: { value: '2026-11-06' } });
		await fireEvent.input(screen.getByLabelText('Opening arrears ($)'), { target: { value: '123.45' } });
		await fireEvent.click(screen.getByLabelText('Co-tenant: Tom'));
		await fireEvent.submit(form());
		await screen.findByText('paid_to_date must be the last day of a rent period');
		expect((screen.getByLabelText('Paid-to date') as HTMLInputElement).value).toBe('2026-11-06');
		expect((screen.getByLabelText('Opening arrears ($)') as HTMLInputElement).value).toBe('123.45');
		expect(screen.getByLabelText('Co-tenant: Tom')).toHaveProperty('checked', true);
	});
	it('guards duplicate submission', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string, p: string) => m === 'POST' ? new Promise((resolve) => { finish = resolve; }) : route(m, p));
		render(Page);
		await fillRent();
		const leaseForm = form();
		await fireEvent.submit(leaseForm);
		await fireEvent.submit(leaseForm);
		expect(apiMock.mock.calls.filter(([m]) => m === 'POST')).toHaveLength(1);
		for (const label of ['Property', 'Tenant', 'Start', 'Frequency', 'Rent ($)', 'Bond ($)', 'Paid-to date', 'Opening arrears ($)']) {
			expect(screen.getByLabelText(label, { exact: true }).matches(':disabled'), label).toBe(true);
		}
		finish({ id: 4 });
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
