import { beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

const page = $state({ params: { org: 'acme' } });
const { beforeNavigateMock } = vi.hoisted(() => ({ beforeNavigateMock: vi.fn() }));
vi.mock('$app/navigation', () => ({ beforeNavigate: beforeNavigateMock }));
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../lib/api', () => ({
	api: apiMock,
	ApiError: class ApiError extends Error {
		constructor(public status: number, message: string) { super(message); }
	}
}));
import { ApiError } from '../../../lib/api';
import Page from './+page.svelte';

const settings = {
	name: 'Acme', abn: '', gst_registered: false, bas_frequency: '',
	bank_bsb: '001002', bank_account: '00123456', payid: 'owner@example.com',
	invoice_lead_days: 0, auto_send_rent: true
};

beforeEach(() => {
	page.params.org = 'acme';
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: [
		{ slug: 'acme', name: 'Acme', role: 'owner', perms: ['org_admin'] },
		{ slug: 'other', name: 'Other', role: 'owner', perms: ['org_admin'] }
	] });
	session.setOrg('acme');
	apiMock.mockReset();
	apiMock.mockImplementation((method: string, _path: string, body: unknown) =>
		Promise.resolve(method === 'GET' ? { ...settings } : body));
});

async function loaded() {
	await vi.waitFor(() => expect((screen.getByLabelText('Organisation name') as HTMLInputElement).value).toBe('Acme'));
}
function form() { return screen.getByRole('button', { name: 'Save settings' }).closest('form')!; }

describe('organisation settings', () => {
	it('cancels drafts back to the last saved values without a write', async () => {
		vi.spyOn(window, 'confirm').mockReturnValue(true);
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('Organisation name'), { target: { value: 'Unsaved' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(screen.getByLabelText('Organisation name')).toHaveProperty('value', 'Acme');
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(0);
	});
	it('protects dirty navigation but does not block session loss', async () => {
		vi.spyOn(window, 'confirm').mockReturnValue(false);
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('Organisation name'), { target: { value: 'Unsaved' } });
		const navigate = beforeNavigateMock.mock.calls.at(-1)![0];
		const cancel = vi.fn();
		navigate({ to: { url: new URL('http://localhost/acme/contacts') }, cancel });
		expect(cancel).toHaveBeenCalledOnce();
		session.clear();
		cancel.mockClear();
		navigate({ to: { url: new URL('http://localhost/login') }, cancel });
		expect(cancel).not.toHaveBeenCalled();
	});
	it('updates only matching cached org name from normalized server response', async () => {
		apiMock.mockImplementation((method: string, _path: string, body: object) =>
			Promise.resolve(method === 'GET' ? { ...settings } : { ...body, name: 'Normalized name' }));
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('Organisation name'), { target: { value: 'Entered name' } });
		await fireEvent.submit(form());
		await screen.findByText('Settings saved.');
		expect(session.me?.orgs.map((o) => [o.slug, o.name])).toEqual([['acme', 'Normalized name'], ['other', 'Other']]);
	});
	it('failed rename leaves cached org name unchanged', async () => {
		apiMock.mockImplementation((method: string) => method === 'GET' ? Promise.resolve({ ...settings }) : Promise.reject(new ApiError(422, 'name rejected')));
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('Organisation name'), { target: { value: 'Failed name' } });
		await fireEvent.submit(form());
		await screen.findByText('name rejected');
		expect(session.currentOrg?.name).toBe('Acme');
	});
	it('loads and saves every field without resetting untouched values', async () => {
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('Organisation name'), { target: { value: 'New name' } });
		await fireEvent.submit(form());
		await screen.findByRole('status');
		expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/settings', {
			name: 'New name', abn: '', gst_registered: false, bas_frequency: '',
			bank_bsb: '001002', bank_account: '00123456', payid: 'owner@example.com',
			invoice_lead_days: 0, auto_send_rent: true
		});
	});

	it('preserves leading zeros, sends numeric bounds and adopts normalized response', async () => {
		apiMock.mockImplementation((method: string, _path: string, body: object) =>
			Promise.resolve(method === 'GET' ? { ...settings } : { ...body, bank_bsb: '001-002' }));
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('Invoice lead days'), { target: { value: '14' } });
		await fireEvent.click(screen.getByLabelText('GST registered'));
		await fireEvent.click(screen.getByLabelText('Send rent invoices automatically'));
		await fireEvent.submit(form());
		await vi.waitFor(() => expect((screen.getByLabelText('BSB') as HTMLInputElement).value).toBe('001-002'));
		expect((screen.getByLabelText('Bank account') as HTMLInputElement).value).toBe('00123456');
		expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/settings', expect.objectContaining({
			invoice_lead_days: 14, gst_registered: true, auto_send_rent: false
		}));
	});

	it('clears optional payment fields explicitly', async () => {
		render(Page);
		await loaded();
		for (const label of ['BSB', 'Bank account', 'PayID']) {
			await fireEvent.input(screen.getByLabelText(label), { target: { value: '' } });
		}
		await fireEvent.submit(form());
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/settings',
			expect.objectContaining({ bank_bsb: '', bank_account: '', payid: '' })));
	});

	it('keeps input and server error after rejected save', async () => {
		apiMock.mockImplementation((method: string) => method === 'GET' ? Promise.resolve({ ...settings }) :
			Promise.reject(new ApiError(422, 'invalid payid')));
		render(Page);
		await loaded();
		await fireEvent.input(screen.getByLabelText('PayID'), { target: { value: 'bad payid' } });
		await fireEvent.submit(form());
		expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'invalid payid');
		expect((screen.getByLabelText('PayID') as HTMLInputElement).value).toBe('bad payid');
	});

	it('does not offer a blank editable form on failed load and supports retry', async () => {
		apiMock.mockRejectedValueOnce(new ApiError(500, 'load failed'));
		render(Page);
		expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'load failed');
		expect(screen.queryByRole('button', { name: 'Save settings' })).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		await loaded();
	});

	it('ignores a late response from previous org', async () => {
		let resolveOld!: (value: unknown) => void;
		apiMock.mockImplementation((_method: string, path: string) => path.includes('/acme/') ?
			new Promise((resolve) => { resolveOld = resolve; }) : Promise.resolve({ ...settings, name: 'Other' }));
		render(Page);
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await vi.waitFor(() => expect((screen.getByLabelText('Organisation name') as HTMLInputElement).value).toBe('Other'));
		resolveOld({ ...settings });
		await Promise.resolve();
		flushSync();
		expect((screen.getByLabelText('Organisation name') as HTMLInputElement).value).toBe('Other');
	});

	it('cannot submit a previous org form to a new route even before effects run', async () => {
		render(Page);
		await loaded();
		const oldForm = form();
		page.params.org = 'other';
		oldForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		expect(apiMock.mock.calls.filter(([method]) => method === 'PUT')).toHaveLength(0);
		flushSync();
	});

	it.each(['staff', 'bookkeeper', 'tenant'])('denies direct editing for %s', async (role) => {
		session.setMe({ user: { id: 1, email: 'user@example.com', name: 'User' }, orgs: [
			{ slug: 'acme', name: 'Acme', role, perms: role === 'tenant' ? [] : ['invoices_read'] }
		] });
		render(Page);
		expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'You do not have permission to manage settings.');
		expect(screen.queryByLabelText('Bank account')).toBeNull();
		expect(apiMock).not.toHaveBeenCalled();
	});

	it('does not trust owner permission from a different org', async () => {
		page.params.org = 'unknown';
		render(Page);
		expect(await screen.findByRole('alert')).toBeTruthy();
		expect(apiMock).not.toHaveBeenCalled();
	});

	it('prevents duplicate submits and disables editing while saving', async () => {
		let finish!: (value: unknown) => void;
		apiMock.mockImplementation((method: string) => method === 'GET' ? Promise.resolve({ ...settings }) :
			new Promise((resolve) => { finish = resolve; }));
		render(Page);
		await loaded();
		const settingsForm = form();
		await fireEvent.submit(settingsForm);
		await fireEvent.submit(settingsForm);
		expect(apiMock.mock.calls.filter(([method]) => method === 'PUT')).toHaveLength(1);
		expect(screen.getByRole('button', { name: 'Saving…' })).toHaveProperty('disabled', true);
		expect((screen.getByLabelText('Organisation name') as HTMLInputElement).closest('fieldset')?.disabled).toBe(true);
		finish({ ...settings });
		await screen.findByText('Settings saved.');
	});
});
