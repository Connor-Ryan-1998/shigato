import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../../lib/test/app.svelte';
import { apiMock } from '../../../lib/test/mock-api';
import { flushSync } from 'svelte';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

import { ApiError } from '../../../lib/api';
import Page from './+page.svelte';

const contact = {
	id: 7, kind: 'tenant', name: 'Tina Tenant', email: 'tina@example.com',
	phone: '0412345678', company_name: 'Example company', abn: '51824753556'
};

beforeEach(() => {
	setPage({ params: { org: 'acme' } });
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: [
		{ slug: 'acme', name: 'Acme', role: 'owner', perms: ['invoices_read', 'contacts_write'] },
		{ slug: 'other', name: 'Other', role: 'owner', perms: ['invoices_read', 'contacts_write'] }
	] });
	session.setOrg('acme');

	apiMock.mockImplementation((method: string, _path: string, body: object) =>
		Promise.resolve(method === 'GET' ? [{ ...contact }] : { id: 8, ...body }));
});

async function name(value = 'New contact') {
	if (!screen.queryByLabelText('Name', { exact: true })) {
		const button = await screen.findByRole('button', { name: 'New contact' });
		await vi.waitFor(() => expect(button).toHaveProperty('disabled', false));
		await fireEvent.click(button);
	}
	await fireEvent.input(await screen.findByLabelText('Name', { exact: true }), { target: { value } });
}
function form() { return screen.getByRole('button', { name: 'Create contact' }).closest('form')!; }

describe('contacts', () => {
	it('edits existing details and clears optional fields without changing kind or provider identity', async () => {
		let saved = false;
		apiMock.mockImplementation((method: string, _path: string, body: object) => {
			if (method === 'PUT') { saved = true; return Promise.resolve({ ...contact, ...body }); }
			return Promise.resolve([saved ? { ...contact, name: 'Corrected', email: '', phone: '', company_name: '', abn: '' } : { ...contact }]);
		});
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit Tina Tenant' }));
		expect((screen.getByLabelText('Name', { exact: true }) as HTMLInputElement).value).toBe('Tina Tenant');
		expect(screen.queryByLabelText('Contact kind')).toBeNull();
		expect(screen.getByText(/does not transfer or revoke portal access/)).toBeTruthy();
		await name('Corrected');
		for (const label of ['Email', 'Phone', 'Company name', 'ABN']) {
			await fireEvent.input(screen.getByLabelText(label, { exact: true }), { target: { value: '' } });
		}
		await fireEvent.submit(screen.getByRole('button', { name: 'Save contact' }).closest('form')!);
		expect(await screen.findByRole('cell', { name: 'Corrected' })).toBeTruthy();
		expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/contacts/7', { name: 'Corrected', email: '', phone: '', company_name: '', abn: '' });
		expect(screen.getByText('Contact updated.')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'New contact' })).toBeTruthy();
	});

	it('ignores late edit completion after org switch', async () => {
		let finish!: (value: unknown) => void;
		apiMock.mockImplementation((m: string, path: string) => m === 'PUT' ? new Promise((resolve) => { finish = resolve; }) : Promise.resolve([{ ...contact, name: path.includes('/other/') ? 'Other contact' : contact.name }]));
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit Tina Tenant' }));
		await fireEvent.submit(screen.getByRole('button', { name: 'Save contact' }).closest('form')!);
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await screen.findByRole('cell', { name: 'Other contact' });
		finish({ ...contact, name: 'Old edit' });
		await Promise.resolve();
		flushSync();
		expect(screen.queryByText('Contact updated.')).toBeNull();
		expect(screen.getByRole('cell', { name: 'Other contact' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'New contact' })).toBeTruthy();
	});

	it('cannot submit or start old-org edit before effects run', async () => {
		render(Page);
		const edit = await screen.findByRole('button', { name: 'Edit Tina Tenant' });
		await fireEvent.click(edit);
		const editForm = screen.getByRole('button', { name: 'Save contact' }).closest('form')!;
		page.params.org = 'other';
		editForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		edit.dispatchEvent(new MouseEvent('click', { bubbles: true }));
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(0);
		session.setOrg('other');
		flushSync();
		expect(screen.queryByLabelText('Name', { exact: true })).toBeNull();
	});
	it('lists all contact details', async () => {
		render(Page);
		const row = await screen.findByRole('row', { name: /Tina Tenant/ });
		for (const value of ['tenant', 'tina@example.com', '0412345678', 'Example company', '51824753556']) {
			expect(within(row).getByRole('cell', { name: value })).toBeTruthy();
		}
	});

	it('filters through API without changing creation kind', async () => {
		render(Page);
		await screen.findByText('Tina Tenant');
		await name();
		await fireEvent.change(screen.getByLabelText('Contact kind'), { target: { value: 'client' } });
		await fireEvent.change(screen.getByLabelText('Filter by kind'), { target: { value: 'supplier' } });
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('GET', '/api/v1/orgs/acme/contacts?kind=supplier'));
		expect((screen.getByLabelText('Contact kind') as HTMLSelectElement).value).toBe('client');
	});

	it.each(['tenant', 'client', 'supplier'])('creates %s with all fields and refreshes list', async (kind) => {
		let created = false;
		apiMock.mockImplementation((method: string, _path: string, body: object) => {
			if (method === 'POST') { created = true; return Promise.resolve({ id: 8, ...body }); }
			return Promise.resolve(created ? [{ ...contact, id: 8, kind, name: 'New contact' }] : []);
		});
		render(Page);
		await name();
		await fireEvent.change(screen.getByLabelText('Contact kind'), { target: { value: kind } });
		for (const [label, value] of [['Email', 'new@example.com'], ['Phone', '0400000000'], ['Company name', 'New Co'], ['ABN', '51824753556']]) {
			await fireEvent.input(screen.getByLabelText(label, { exact: true }), { target: { value } });
		}
		await fireEvent.submit(form());
		expect(await screen.findByRole('cell', { name: 'New contact' })).toBeTruthy();
		expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/contacts', {
			kind, name: 'New contact', email: 'new@example.com', phone: '0400000000', company_name: 'New Co', abn: '51824753556'
		});
		expect(screen.queryByLabelText('Name', { exact: true })).toBeNull();
	});

	it('handles empty list and missing optional fields', async () => {
		apiMock.mockResolvedValueOnce([]).mockResolvedValue([{ id: 9, name: 'Minimal', kind: 'client' }]);
		render(Page);
		expect(await screen.findByText('No contacts found.')).toBeTruthy();
		await fireEvent.change(screen.getByLabelText('Filter by kind'), { target: { value: 'client' } });
		expect(await screen.findByRole('cell', { name: 'Minimal' })).toBeTruthy();
		expect(screen.queryByRole('alert')).toBeNull();
	});

	it('reports success when created contact is outside current filter', async () => {
		apiMock.mockImplementation((method: string, _path: string, body: object) =>
			Promise.resolve(method === 'GET' ? [] : { id: 8, ...body }));
		render(Page);
		await name();
		await fireEvent.change(screen.getByLabelText('Filter by kind'), { target: { value: 'supplier' } });
		await fireEvent.change(screen.getByLabelText('Contact kind'), { target: { value: 'tenant' } });
		await fireEvent.submit(form());
		expect(await screen.findByText('Contact created.')).toBeTruthy();
		expect(await screen.findByText('No contacts found.')).toBeTruthy();
		expect(apiMock).toHaveBeenLastCalledWith('GET', '/api/v1/orgs/acme/contacts?kind=supplier');
	});

	it('allows bookkeeper browsing but not creation', async () => {
		session.setMe({ user: { id: 1, email: 'book@example.com', name: 'Bookkeeper' }, orgs: [
			{ slug: 'acme', name: 'Acme', role: 'bookkeeper', perms: ['invoices_read'] }
		] });
		render(Page);
		expect(await screen.findByText('Tina Tenant')).toBeTruthy();
		expect(screen.queryByLabelText('Name', { exact: true })).toBeNull();
		expect(screen.queryByRole('button', { name: /Edit Tina/ })).toBeNull();
	});

	it('denies staff contact screen to portal users and mismatched session org', async () => {
		page.params.org = 'unknown';
		render(Page);
		expect(await screen.findByRole('alert')).toBeTruthy();
		expect(apiMock).not.toHaveBeenCalled();
	});

	it('ignores old org responses', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((_method: string, path: string) => path.includes('/acme/') ?
			new Promise((resolve) => { finish = resolve; }) : Promise.resolve([{ ...contact, name: 'Other tenant' }]));
		render(Page);
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await screen.findByText('Other tenant');
		finish([{ ...contact }]);
		await Promise.resolve();
		flushSync();
		expect(screen.queryByText('Tina Tenant')).toBeNull();
		expect(screen.getByText('Other tenant')).toBeTruthy();
	});

	it('ignores old filter responses', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((_method: string, path: string) => path.endsWith('kind=tenant') ?
			new Promise((resolve) => { finish = resolve; }) : Promise.resolve([{ ...contact, name: 'Current contact' }]));
		render(Page);
		await screen.findByText('Current contact');
		await fireEvent.change(screen.getByLabelText('Filter by kind'), { target: { value: 'tenant' } });
		await fireEvent.change(screen.getByLabelText('Filter by kind'), { target: { value: 'client' } });
		await screen.findByText('Current contact');
		finish([{ ...contact }]);
		await Promise.resolve();
		flushSync();
		expect(screen.queryByText('Tina Tenant')).toBeNull();
	});

	it('cannot submit old org draft to new org before effects run', async () => {
		render(Page);
		await name();
		const oldForm = form();
		page.params.org = 'other';
		oldForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		expect(apiMock.mock.calls.filter(([method]) => method === 'POST')).toHaveLength(0);
		session.setOrg('other');
		flushSync();
		expect(screen.queryByLabelText('Name', { exact: true })).toBeNull();
	});

	it('reports failed GET separately from empty state and supports retry', async () => {
		apiMock.mockRejectedValueOnce(new ApiError(500, 'load failed'));
		render(Page);
		expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'load failed');
		expect(screen.queryByText('No contacts found.')).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		expect(await screen.findByText('Tina Tenant')).toBeTruthy();
	});
});
