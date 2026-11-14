import { describe, expect, it, vi } from 'vitest';
import { apiMock } from '../../test/mock-api';
import { fireEvent, render, screen } from '@testing-library/svelte';

import ProjectDialog from './ProjectDialog.svelte';
import type { Project } from './types';

const project: Project = {
	id: 5, client_contact_id: 9, name: 'Website', hourly_rate: 12000, gst_code: 'GST', status: 'in_progress',
	description: 'Old', start_date: '2026-01-01', end_date: null, billing_method: 'tm', active: true
};
const contacts = [{ id: 9, name: 'Rizzware', kind: 'client' }, { id: 10, name: 'Other', kind: 'client' }, { id: 11, name: 'Tenant', kind: 'tenant' }];

describe('ProjectDialog', () => {
	it('sends a full replace on edit', async () => {
		apiMock.mockResolvedValue(project);
		const onsaved = vi.fn();
		render(ProjectDialog, { open: true, org: 'acme', contacts, project, onsaved });
		expect(screen.queryByRole('option', { name: 'Tenant' })).toBeNull();
		await fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'completed' } });
		await fireEvent.change(screen.getByLabelText('Billing method'), { target: { value: 'fixed' } });
		await fireEvent.input(screen.getByLabelText('Base rate ($/h)'), { target: { value: '150.50' } });
		await fireEvent.input(screen.getByLabelText('Start date'), { target: { value: '' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save project' }));
		await vi.waitFor(() => expect(onsaved).toHaveBeenCalled());
		expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/projects/5', {
			name: 'Website', client_contact_id: 9, hourly_rate: 15050, gst_code: 'GST', status: 'completed',
			description: 'Old', start_date: null, end_date: null, billing_method: 'fixed'
		});
	});

	it('rejects a bad rate without calling the API', async () => {
		render(ProjectDialog, { open: true, org: 'acme', contacts, onsaved: vi.fn() });
		await fireEvent.input(screen.getByLabelText('Project name'), { target: { value: 'New' } });
		await fireEvent.input(screen.getByLabelText('Base rate ($/h)'), { target: { value: '0' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Create project' }));
		await screen.findByText('Base rate must be greater than zero.');
		expect(apiMock).not.toHaveBeenCalled();
	});
});
