import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../api', () => ({
	api: apiMock,
	ApiError: class ApiError extends Error { constructor(public status: number, message: string) { super(message); } }
}));
import LogTimeDialog from './LogTimeDialog.svelte';
import type { Entry, Project } from './types';

const project = (id: number, name: string, active = true): Project => ({
	id, name, client_contact_id: 2, hourly_rate: 15000, gst_code: 'GST', status: active ? 'in_progress' : 'completed',
	description: '', start_date: null, end_date: null, billing_method: 'tm', active
});
const projects = [project(3, 'Website'), project(4, 'Closed', false)];
const entry: Entry = {
	id: 5, task_id: 12, user_id: 1, work_date: '2026-11-01', minutes: 90, note: 'Design', task_name: 'Old',
	project_id: 4, project_name: 'Closed', billing_method: 'tm', client_contact_id: 2, client_name: 'C', user_name: 'U', rate: 15000
};

beforeEach(() => {
	HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) { this.setAttribute('open', ''); });
	HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) { this.removeAttribute('open'); });
	apiMock.mockReset();
	apiMock.mockImplementation((m: string, p: string) => {
		if (p.endsWith('/projects/3/tasks')) return Promise.resolve([{ id: 11, project_id: 3, name: 'Build', active: true }]);
		if (p.endsWith('/projects/4/tasks')) return Promise.resolve([{ id: 12, project_id: 4, name: 'Old', active: false }, { id: 13, project_id: 4, name: 'Gone', active: false }]);
		return Promise.resolve({});
	});
});

describe('LogTimeDialog', () => {
	it('creates an entry with a parsed H:MM duration', async () => {
		const onsaved = vi.fn();
		render(LogTimeDialog, { props: { open: true, org: 'acme', projects, onsaved } });
		expect(screen.queryByRole('option', { name: 'Closed' })).toBeNull();
		await screen.findByRole('option', { name: 'Build' });
		await fireEvent.input(screen.getByLabelText('Date'), { target: { value: '2026-11-02' } });
		await fireEvent.input(screen.getByLabelText('Time (H:MM)'), { target: { value: '2:45' } });
		await fireEvent.input(screen.getByLabelText('Note'), { target: { value: 'Ingress' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Log time' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/time-entries', {
			task_id: 11, work_date: '2026-11-02', minutes: 165, note: 'Ingress'
		}));
		await vi.waitFor(() => expect(onsaved).toHaveBeenCalledOnce());
	});

	it('edits an entry, keeping its closed project and inactive task selectable', async () => {
		render(LogTimeDialog, { props: { open: true, org: 'acme', projects, entry, onsaved: vi.fn() } });
		await screen.findByRole('option', { name: 'Old' });
		expect(screen.getByRole('option', { name: 'Closed' })).toBeTruthy();
		expect(screen.queryByRole('option', { name: 'Gone' })).toBeNull();
		expect((screen.getByLabelText('Time (H:MM)') as HTMLInputElement).value).toBe('01:30');
		await fireEvent.input(screen.getByLabelText('Time (H:MM)'), { target: { value: '45' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save entry' }));
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/time-entries/5', {
			task_id: 12, work_date: '2026-11-01', minutes: 45, note: 'Design'
		}));
	});

	it('rejects an invalid duration without a request', async () => {
		render(LogTimeDialog, { props: { open: true, org: 'acme', projects, onsaved: vi.fn() } });
		await screen.findByRole('option', { name: 'Build' });
		await fireEvent.input(screen.getByLabelText('Time (H:MM)'), { target: { value: '25:00' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Log time' }));
		expect(screen.getByRole('alert').textContent).toContain('H:MM');
		expect(apiMock.mock.calls.filter(([m]) => m !== 'GET')).toHaveLength(0);
	});

	it('keeps the dialog open with the server error', async () => {
		const { ApiError } = await import('../../api');
		apiMock.mockImplementation((m: string) => m === 'POST' ? Promise.reject(new ApiError(422, 'work_date must not be after today')) : Promise.resolve([{ id: 11, project_id: 3, name: 'Build', active: true }]));
		render(LogTimeDialog, { props: { open: true, org: 'acme', projects, onsaved: vi.fn() } });
		await screen.findByRole('option', { name: 'Build' });
		await fireEvent.input(screen.getByLabelText('Time (H:MM)'), { target: { value: '1:00' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Log time' }));
		expect(await screen.findByText('work_date must not be after today')).toBeTruthy();
	});
});
