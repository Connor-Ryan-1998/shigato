import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { session } from '../../../../lib/session.svelte';

const page = $state({ params: { org: 'acme', id: '5' }, url: new URL('http://x/acme/projects/5') });
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { apiMock, gotoMock } = vi.hoisted(() => ({ apiMock: vi.fn(), gotoMock: vi.fn() }));
vi.mock('$app/navigation', () => ({ goto: gotoMock, beforeNavigate: vi.fn() }));
vi.mock('../../../../lib/api', () => ({
	api: apiMock,
	ApiError: class ApiError extends Error { constructor(public status: number, message: string) { super(message); } }
}));
import Page from './+page.svelte';

HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) { this.open = true; };
HTMLDialogElement.prototype.close = function (this: HTMLDialogElement) { this.open = false; };

const project = {
	id: 5, client_contact_id: 9, name: 'HTM Data Platform', hourly_rate: 12000, gst_code: 'GST', status: 'in_progress',
	description: '', start_date: null, end_date: null, billing_method: 'tm', active: true
};
const entry = (id: number, task_id: number, minutes: number, extra = {}) => ({
	id, task_id, user_id: 1, work_date: '2026-10-01', minutes, note: '', task_name: `Task ${task_id}`, project_id: 5,
	project_name: project.name, billing_method: 'tm', client_contact_id: 9, client_name: 'Rizzware', user_name: 'Connor', rate: 12000, ...extra
});
const entries = [entry(1, 1, 120, { invoice_line_id: 7 }), entry(2, 1, 30), entry(3, 2, 45, { rate: 20000 })];

function route(m: string, p: string): Promise<unknown> {
	if (m !== 'GET') return Promise.resolve({});
	if (p.endsWith('/projects/5')) return Promise.resolve(project);
	if (p.endsWith('/tasks')) return Promise.resolve([{ id: 1, project_id: 5, name: 'Task 1', active: true }, { id: 2, project_id: 5, name: 'Task 2', hourly_rate: 20000, active: true }]);
	if (p.includes('/time-entries')) return Promise.resolve({ entries: p.includes('billed=true') ? [entries[0]] : entries, total_minutes: 0 });
	return Promise.resolve([{ id: 9, name: 'Rizzware', kind: 'client' }]);
}

beforeEach(() => {
	session.setMe({ user: { id: 1, email: 'o@example.com', name: 'Owner' }, orgs: [{ slug: 'acme', name: 'Acme', role: 'owner', perms: ['timesheets_read', 'timesheets_write', 'timesheets_admin'] }] });
	session.setOrg('acme');
	apiMock.mockReset();
	apiMock.mockImplementation(route);
	gotoMock.mockReset();
});

describe('project detail', () => {
	it('shows overview stats from all entries', async () => {
		render(Page);
		await screen.findByRole('heading', { name: 'HTM Data Platform' });
		expect(screen.getAllByText('Rizzware').length).toBeGreaterThan(0);
		const stat = (label: string) => screen.getByText(`${label} hours`, { selector: '.stat-title' }).parentElement!;
		// 120 + 30 + 45 minutes; values 120m@$120 + 30m@$120 + 45m@$200.
		expect(within(stat('Logged')).getByText('03:15')).toBeTruthy();
		expect(within(stat('Logged')).getByText('$450.00')).toBeTruthy();
		expect(within(stat('Billed')).getByText('02:00')).toBeTruthy();
		expect(within(stat('Billed')).getByText('$240.00')).toBeTruthy();
		expect(within(stat('Unbilled')).getByText('01:15')).toBeTruthy();
		expect(within(stat('Unbilled')).getByText('$210.00')).toBeTruthy();
		const users = screen.getByRole('heading', { name: 'Users' }).closest('section')!;
		expect(within(users).getByText('Connor').closest('tr')!.textContent).toContain('03:1502:0001:15');
	});

	it('switches to the timesheet tab and filters by status', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('tab', { name: 'Timesheet' }));
		expect(gotoMock).toHaveBeenCalledWith('?tab=timesheet', expect.objectContaining({ replaceState: true }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('GET', '/api/v1/orgs/acme/time-entries?project_id=5&unbilled=true'));
		await fireEvent.change(screen.getByLabelText('Billing status'), { target: { value: 'billed' } });
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('GET', '/api/v1/orgs/acme/time-entries?project_id=5&billed=true'));
		await screen.findByText('Invoiced', { selector: 'td' });
	});

	it('adds a task with an optional rate override', async () => {
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: '+ Add task' }));
		await fireEvent.input(screen.getByLabelText('Task name'), { target: { value: 'Phase 2' } });
		await fireEvent.input(screen.getByLabelText('Task hourly rate ($)'), { target: { value: '99.50' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Add task' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/projects/5/tasks', { name: 'Phase 2', hourly_rate: 9950 }));
	});

	it('hides admin and write actions without perms', async () => {
		session.setMe({ user: { id: 1, email: 'o@example.com', name: 'Owner' }, orgs: [{ slug: 'acme', name: 'Acme', role: 'viewer', perms: ['timesheets_read'] }] });
		session.setOrg('acme');
		render(Page);
		await screen.findByRole('heading', { name: 'HTM Data Platform' });
		expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Log Time' })).toBeNull();
		expect(screen.queryByRole('button', { name: '+ Add task' })).toBeNull();
	});
});
