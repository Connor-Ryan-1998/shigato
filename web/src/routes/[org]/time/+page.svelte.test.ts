import { beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { act, fireEvent, render, screen, within } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

const page = $state({ params: { org: 'acme' } });
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../lib/api', () => ({
	api: apiMock,
	ApiError: class ApiError extends Error { constructor(public status: number, message: string) { super(message); } }
}));
import { ApiError } from '../../../lib/api';
import Page from './+page.svelte';

const project = { id: 3, name: 'Website', client_contact_id: 2, hourly_rate: 15000, gst_code: 'GST', active: true };
const tasks = [{ id: 11, project_id: 3, name: 'Build', active: true }, { id: 12, project_id: 3, name: 'Old task', active: false }];
const entry = { id: 5, task_id: 11, user_id: 1, work_date: '2026-11-01', minutes: 90, note: 'Design' };

function login(role = 'owner') {
	const perms = ['timesheets_read', ...(role === 'bookkeeper' ? [] : ['timesheets_write']), ...(role === 'owner' ? ['timesheets_admin'] : [])];
	session.setMe({ user: { id: 1, email: 'user@example.com', name: 'User' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role, perms })) });
	session.setOrg('acme');
}
function route(method: string, path: string, body?: object): Promise<unknown> {
	if (method !== 'GET') return Promise.resolve({ ...entry, ...body });
	if (/\/projects(?:\?|$)/.test(path)) return Promise.resolve([{ ...project }, { ...project, id: 4, name: 'Second' }]);
	if (path.endsWith('/tasks')) return Promise.resolve(tasks.map((t) => ({ ...t })));
	if (path.includes('/time-entries?')) return Promise.resolve({ entries: [{ ...entry }], total_minutes: 90 });
	if (path.endsWith('/contacts')) return Promise.resolve([{ id: 2, kind: 'client', name: 'Client' }]);
	return Promise.reject(new Error(`Unexpected ${method} ${path}`));
}
beforeEach(() => {
	page.params.org = 'acme';
	login();
	apiMock.mockReset();
	apiMock.mockImplementation(route);
});
async function edit() {
	await fireEvent.click(await screen.findByRole('button', { name: 'Edit entry 5' }));
	return screen.getByRole('form', { name: 'Edit time entry' });
}

describe('time entry workflows', () => {
	it.each([['Log time', 'Note'], ['Add task', 'New task'], ['Create project', 'Name']])('locks %s draft while saving', async (button, label) => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string, p: string, b: object) => m === 'POST' ? new Promise((resolve) => { finish = resolve; }) : route(m, p, b));
		render(Page);
		await screen.findByText('Design');
		const form = screen.getByRole('button', { name: button }).closest('form')!;
		await fireEvent.submit(form);
		expect(screen.getByLabelText(label, { exact: true }).matches(':disabled')).toBe(true);
		finish({ ...project, id: 5 });
	});
	it('saves full entry and can clear note', async () => {
		render(Page);
		const form = await edit();
		await fireEvent.input(within(form).getByLabelText('Entry minutes'), { target: { value: '45' } });
		await fireEvent.input(within(form).getByLabelText('Entry date'), { target: { value: '2026-11-02' } });
		await fireEvent.input(within(form).getByLabelText('Entry note'), { target: { value: '' } });
		await fireEvent.submit(form);
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/time-entries/5', {
			task_id: 11, work_date: '2026-11-02', minutes: 45, note: ''
		}));
	});
	it('cancel does not write', async () => {
		render(Page);
		const form = await edit();
		await fireEvent.input(within(form).getByLabelText('Entry minutes'), { target: { value: '45' } });
		await fireEvent.click(within(form).getByRole('button', { name: 'Cancel' }));
		expect(screen.queryByRole('form', { name: 'Edit time entry' })).toBeNull();
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(0);
	});
	it.each([409, 422])('keeps draft and error after %s', async (status) => {
		apiMock.mockImplementation((m: string, p: string, b: object) => m === 'PUT' ? Promise.reject(new ApiError(status, 'entry cannot be changed')) : route(m, p, b));
		render(Page);
		const form = await edit();
		await fireEvent.input(within(form).getByLabelText('Entry note'), { target: { value: 'Retained' } });
		await fireEvent.submit(form);
		await screen.findByText('entry cannot be changed');
		expect((within(form).getByLabelText('Entry note') as HTMLInputElement).value).toBe('Retained');
	});
	it('staff can edit only own unbilled entries', async () => {
		login('staff');
		apiMock.mockImplementation((m: string, p: string, b: object) => p.includes('/time-entries?') ? Promise.resolve({ entries: [entry, { ...entry, id: 6, user_id: 2 }, { ...entry, id: 7, invoice_line_id: 9 }], total_minutes: 270 }) : route(m, p, b));
		render(Page);
		await screen.findByRole('button', { name: 'Edit entry 5' });
		expect(screen.queryByRole('button', { name: 'Edit entry 6' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Edit entry 7' })).toBeNull();
		expect(screen.getAllByRole('button', { name: 'Delete' })).toHaveLength(1);
	});
	it('bookkeepers cannot log, edit or delete entries', async () => {
		login('bookkeeper');
		render(Page);
		await screen.findByText('Design');
		expect(screen.queryByRole('button', { name: 'Log time' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Edit entry 5' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
	});
	it('can reselect original inactive task without losing other edits', async () => {
		apiMock.mockImplementation((m: string, p: string, b: object) => {
			if (m === 'GET' && p.endsWith('/tasks')) return Promise.resolve([...tasks, { id: 13, project_id: 3, name: 'Other inactive task', active: false }]);
			return p.includes('/time-entries?') ? Promise.resolve({ entries: [{ ...entry, task_id: 12 }], total_minutes: 90 }) : route(m, p, b);
		});
		render(Page);
		const form = await edit();
		const select = within(form).getByLabelText('Entry task') as HTMLSelectElement;
		expect(select.value).toBe('12');
		await fireEvent.input(within(form).getByLabelText('Entry note'), { target: { value: 'Retained note' } });
		await fireEvent.change(select, { target: { value: '11' } });
		expect(within(select).getByRole('option', { name: 'Old task' })).toBeTruthy();
		expect(within(select).queryByRole('option', { name: 'Other inactive task' })).toBeNull();
		await fireEvent.change(select, { target: { value: '12' } });
		expect(select.value).toBe('12');
		expect((within(form).getByLabelText('Entry note') as HTMLInputElement).value).toBe('Retained note');
		await fireEvent.submit(form);
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/time-entries/5', {
			task_id: 12, work_date: '2026-11-01', minutes: 90, note: 'Retained note'
		}));
	});
	it('filters inclusive dates and clears either bound', async () => {
		render(Page);
		await screen.findByText('Design');
		await fireEvent.input(screen.getByLabelText('Entries from'), { target: { value: '2026-11-01' } });
		await fireEvent.input(screen.getByLabelText('Entries to'), { target: { value: '2026-11-02' } });
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('GET', '/api/v1/orgs/acme/time-entries?project_id=3&from=2026-11-01&to=2026-11-02'));
		await fireEvent.input(screen.getByLabelText('Entries from'), { target: { value: '' } });
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('GET', '/api/v1/orgs/acme/time-entries?project_id=3&to=2026-11-02'));
	});
	it('rejects reversed date filter without request', async () => {
		render(Page);
		await screen.findByText('Design');
		await fireEvent.input(screen.getByLabelText('Entries from'), { target: { value: '2026-11-03' } });
		await fireEvent.input(screen.getByLabelText('Entries to'), { target: { value: '2026-11-01' } });
		await screen.findByText('Entries from must not be after entries to.');
		expect(apiMock.mock.calls.some(([, p]) => p.includes('from=2026-11-03&to=2026-11-01'))).toBe(false);
	});
	it('cannot submit old entry when project changes', async () => {
		render(Page);
		const form = await edit();
		const select = screen.getByLabelText('Project') as HTMLSelectElement;
		select.value = '4';
		select.dispatchEvent(new Event('change', { bubbles: true }));
		form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(0);
		flushSync();
	});
	it('ignores old org response', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string, p: string, b: object) => p.includes('/acme/time-entries?') ? new Promise((resolve) => { finish = resolve; }) : route(m, p, b));
		render(Page);
		await vi.waitFor(() => expect(finish).toBeTypeOf('function'));
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await screen.findByText('Design');
		finish({ entries: [{ ...entry, note: 'Old org note' }], total_minutes: 90 });
		await Promise.resolve();
		flushSync();
		expect(screen.queryByText('Old org note')).toBeNull();
	});
	it('prevents duplicate saves', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string, p: string, b: object) => m === 'PUT' ? new Promise((resolve) => { finish = resolve; }) : route(m, p, b));
		render(Page);
		const form = await edit();
		await fireEvent.submit(form);
		await fireEvent.submit(form);
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(1);
		finish(entry);
		await vi.waitFor(() => expect(screen.queryByRole('form', { name: 'Edit time entry' })).toBeNull());
	});
});

describe('project and task workflows', () => {
	it.each([false, true])('ignores superseded archive-filter failure with current filter %s', async (currentFilter) => {
		let rejectOld!: (reason: unknown) => void;
		let allRequests = 0;
		apiMock.mockImplementation((m: string, p: string, b: object) => {
			if (m === 'GET' && p.endsWith('/projects') && ++allRequests === 1) {
				return new Promise((_resolve, reject) => { rejectOld = reject; });
			}
			return route(m, p, b);
		});
		render(Page);
		await screen.findByText('Design');
		const toggle = screen.getByLabelText('Show archived projects');
		await fireEvent.click(toggle);
		await fireEvent.click(toggle);
		if (currentFilter) await fireEvent.click(toggle);
		expect(toggle).toHaveProperty('checked', currentFilter);
		await act(() => { rejectOld(new ApiError(500, 'Obsolete project load failed')); });
		expect(screen.queryByText('Obsolete project load failed')).toBeNull();
		expect(screen.getByText('Design')).toBeTruthy();
	});
	it('still reports current archive-filter failure', async () => {
		apiMock.mockImplementation((m: string, p: string, b: object) => m === 'GET' && p.endsWith('/projects') ?
			Promise.reject(new ApiError(500, 'Current project load failed')) : route(m, p, b));
		render(Page);
		await screen.findByText('Design');
		await fireEvent.click(screen.getByLabelText('Show archived projects'));
		expect(await screen.findByText('Current project load failed')).toBeTruthy();
	});
	it('edits project name and rate while preserving GST and active state', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit project' }));
		await fireEvent.input(screen.getByLabelText('Project name'), { target: { value: 'Renamed' } });
		await fireEvent.input(screen.getByLabelText('Project hourly rate ($)'), { target: { value: '175.50' } });
		await fireEvent.submit(screen.getByRole('form', { name: 'Edit project' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/projects/3', {
			name: 'Renamed', hourly_rate: 17550, gst_code: 'GST', active: true
		}));
	});
	it.each([['125.50', 12550], ['', null]])('sends task override %s as %s', async (input, expected) => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit task 11' }));
		await fireEvent.input(screen.getByLabelText('Task name'), { target: { value: 'Updated task' } });
		await fireEvent.input(screen.getByLabelText('Task hourly rate ($)'), { target: { value: input } });
		await fireEvent.submit(screen.getByRole('form', { name: 'Edit task' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/tasks/11', {
			name: 'Updated task', hourly_rate: expected, active: true
		}));
	});
	it('rejects zero override without treating it as inherited rate', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit task 11' }));
		await fireEvent.input(screen.getByLabelText('Task hourly rate ($)'), { target: { value: '0' } });
		await fireEvent.submit(screen.getByRole('form', { name: 'Edit task' }));
		await screen.findByText('Task hourly rate must be greater than zero.');
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(0);
		expect((screen.getByLabelText('Task hourly rate ($)') as HTMLInputElement).value).toBe('0');
	});
	it('archives tasks using all required fields', async () => {
		render(Page);
		await screen.findByText('Design');
		const task = screen.getAllByRole('listitem').find((li) => li.textContent?.includes('Build'))!;
		await fireEvent.click(within(task).getByRole('button', { name: 'Archive' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/tasks/11', { name: 'Build', hourly_rate: null, active: false }));
	});
	it('archives projects using all required fields', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Archive project' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/projects/3', { name: 'Website', hourly_rate: 15000, gst_code: 'GST', active: false }));
	});
	it('shows and restores archived projects without allowing new logging', async () => {
		apiMock.mockImplementation((m: string, p: string, b: object) => m === 'GET' && p.endsWith('/projects') ? Promise.resolve([{ ...project, active: false }]) : route(m, p, b));
		render(Page);
		await screen.findByText('Design');
		await fireEvent.click(screen.getByLabelText('Show archived projects'));
		await fireEvent.click(await screen.findByRole('button', { name: 'Restore project' }));
		expect(screen.queryByRole('button', { name: 'Log time' })).toBeNull();
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/projects/3', { name: 'Website', hourly_rate: 15000, gst_code: 'GST', active: true }));
	});
	it('keeps failed project draft and cancels without additional writes', async () => {
		apiMock.mockImplementation((m: string, p: string, b: object) => m === 'PUT' ? Promise.reject(new ApiError(422, 'duplicate name')) : route(m, p, b));
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Edit project' }));
		await fireEvent.input(screen.getByLabelText('Project name'), { target: { value: 'Conflicting name' } });
		const form = screen.getByRole('form', { name: 'Edit project' });
		await fireEvent.submit(form);
		await screen.findByText('duplicate name');
		expect((screen.getByLabelText('Project name') as HTMLInputElement).value).toBe('Conflicting name');
		await fireEvent.click(within(form).getByRole('button', { name: 'Cancel' }));
		expect(apiMock.mock.calls.filter(([m]) => m === 'PUT')).toHaveLength(1);
	});
	it('staff have no project/task editors', async () => {
		login('staff');
		render(Page);
		await screen.findByText('Design');
		expect(screen.queryByRole('button', { name: 'Edit project' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Edit task 11' })).toBeNull();
	});
});
