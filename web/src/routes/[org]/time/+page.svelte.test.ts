import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../../lib/test/app.svelte';
import { apiMock } from '../../../lib/test/mock-api';
import { flushSync } from 'svelte';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

import Page from './+page.svelte';

const project = {
	id: 3, name: 'Website', client_contact_id: 2, hourly_rate: 15000, gst_code: 'GST', status: 'in_progress',
	description: '', start_date: null, end_date: null, billing_method: 'tm', active: true
};
const base = {
	task_id: 11, task_name: 'Build', project_id: 3, project_name: 'Website', billing_method: 'tm',
	client_contact_id: 2, client_name: 'Rizzware', rate: 15000, note: ''
};
const entries = [
	{ ...base, id: 5, user_id: 1, user_name: 'Me', work_date: '2026-11-01', minutes: 90, note: 'Design' },
	{ ...base, id: 6, user_id: 2, user_name: 'Other', work_date: '2026-11-02', minutes: 30 },
	{ ...base, id: 7, user_id: 1, user_name: 'Me', work_date: '2026-11-03', minutes: 45, invoice_line_id: 9 },
	{ ...base, id: 8, user_id: 1, user_name: 'Me', work_date: '2026-11-04', minutes: 60, project_id: 4,
		project_name: 'Fixed job', billing_method: 'fixed', client_contact_id: 5, client_name: 'Acme Co' }
];

function login(role = 'owner') {
	const perms = ['timesheets_read', ...(role === 'bookkeeper' ? [] : ['timesheets_write']), ...(role === 'owner' ? ['timesheets_admin'] : [])];
	session.setMe({ user: { id: 1, email: 'user@example.com', name: 'User' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role, perms })) });
	session.setOrg('acme');
}
function route(method: string, path: string): Promise<unknown> {
	if (method !== 'GET') return Promise.resolve({});
	if (path.endsWith('/projects')) return Promise.resolve([{ ...project }]);
	if (path.endsWith('/tasks')) return Promise.resolve([{ id: 11, project_id: 3, name: 'Build', active: true }]);
	if (path.includes('/time-entries?')) return Promise.resolve({ entries: entries.map((e) => ({ ...e })), total_minutes: 225 });
	return Promise.reject(new Error(`Unexpected ${method} ${path}`));
}
const entryCalls = () => apiMock.mock.calls.filter(([m, p]) => m === 'GET' && p.includes('/time-entries?')).map(([, p]) => p);

beforeEach(() => {
	setPage({ params: { org: 'acme' } });
	login();

	apiMock.mockImplementation(route);
});

describe('time logging page', () => {
	it('defaults to unbilled time and lists entries newest first with billing status', async () => {
		render(Page);
		await screen.findByText('Design');
		expect(screen.getByRole('heading', { name: 'Timesheets Yet to be Invoiced' })).toBeTruthy();
		expect(entryCalls()[0]).toBe('/api/v1/orgs/acme/time-entries?unbilled=true');
		const rows = screen.getAllByRole('row').slice(1, -1);
		expect(within(rows[0]).getByText('Fixed fee')).toBeTruthy();
		expect(within(rows[1]).getByText('Invoiced')).toBeTruthy();
		expect(within(rows[3]).getByText('Unbilled')).toBeTruthy();
		expect(screen.getByRole('link', { name: 'Fixed job' }).getAttribute('href')).toBe('/acme/projects/4');
		expect(screen.getByText('03:45')).toBeTruthy();
	});

	it('switches status and period through server params', async () => {
		render(Page);
		await screen.findByText('Design');
		await fireEvent.click(screen.getByRole('button', { name: 'Timesheets Invoiced' }));
		await vi.waitFor(() => expect(entryCalls().at(-1)).toBe('/api/v1/orgs/acme/time-entries?billed=true'));
		await fireEvent.click(screen.getByRole('button', { name: 'All Timesheets' }));
		await fireEvent.change(screen.getByLabelText('Period'), { target: { value: 'this_month' } });
		await vi.waitFor(() => expect(entryCalls().at(-1)).toMatch(/time-entries\?from=\d{4}-\d{2}-01&to=\d{4}-\d{2}-\d{2}$/));
	});

	it('filters customer, project and user client-side', async () => {
		render(Page);
		await screen.findByText('Design');
		await fireEvent.change(screen.getByLabelText('Customer'), { target: { value: '5' } });
		expect(screen.queryByText('Design')).toBeNull();
		expect(screen.getByRole('link', { name: 'Fixed job' })).toBeTruthy();
		await fireEvent.change(screen.getByLabelText('Customer'), { target: { value: '' } });
		await fireEvent.change(screen.getByLabelText('User'), { target: { value: '2' } });
		expect(screen.getAllByRole('row')).toHaveLength(3);
		expect(within(screen.getByLabelText('Project')).getAllByRole('option').map((o) => o.textContent)).toEqual(['All', 'Fixed job', 'Website']);
	});

	it('staff edit and delete only their own unbilled entries', async () => {
		login('staff');
		render(Page);
		await screen.findByText('Design');
		expect(screen.getByRole('button', { name: 'Edit entry 5' })).toBeTruthy();
		expect(screen.queryByRole('button', { name: 'Edit entry 6' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Edit entry 7' })).toBeNull();
	});

	it('bookkeepers cannot log, edit or delete', async () => {
		login('bookkeeper');
		render(Page);
		await screen.findByText('Design');
		expect(screen.queryByRole('button', { name: '+ New Log Entry' })).toBeNull();
		expect(screen.queryByRole('button', { name: /Edit entry/ })).toBeNull();
		expect(screen.queryByRole('button', { name: /Delete entry/ })).toBeNull();
	});

	it('deletes after confirmation and reloads', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Delete entry 5' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('DELETE', '/api/v1/orgs/acme/time-entries/5'));
		await vi.waitFor(() => expect(entryCalls()).toHaveLength(2));
	});

	it('opens the edit dialog prefilled', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit entry 5' }));
		expect((screen.getByLabelText('Time (H:MM)') as HTMLInputElement).value).toBe('01:30');
		expect((screen.getByLabelText('Note') as HTMLTextAreaElement).value).toBe('Design');
	});

	it('shows a permission message without timesheets_read', async () => {
		session.setMe({ user: { id: 1, email: 'u@example.com', name: 'U' }, orgs: [{ slug: 'acme', name: 'acme', role: 'staff', perms: [] }] });
		session.setOrg('acme');
		render(Page);
		expect(screen.getByRole('alert').textContent).toContain('permission');
		expect(apiMock).not.toHaveBeenCalled();
	});

	it('ignores an old org response', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string, p: string) => p.includes('/acme/time-entries?') ? new Promise((resolve) => { finish = resolve; }) : route(m, p));
		render(Page);
		await vi.waitFor(() => expect(finish).toBeTypeOf('function'));
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await screen.findByText('Design');
		finish({ entries: [{ ...entries[0], id: 99, note: 'Old org note' }] });
		await Promise.resolve();
		flushSync();
		expect(screen.queryByText('Old org note')).toBeNull();
	});
});
