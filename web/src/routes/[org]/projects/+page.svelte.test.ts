import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../../lib/test/app.svelte';
import { apiMock } from '../../../lib/test/mock-api';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

import Page from './+page.svelte';

const project = (id: number, name: string, status: string, extra = {}) => ({
	id, name, status, client_contact_id: 9, hourly_rate: 12000, gst_code: 'GST', description: '',
	start_date: null, end_date: null, billing_method: 'tm', active: status === 'pending' || status === 'in_progress', ...extra
});
const projects = [
	project(1, 'Website', 'in_progress'),
	project(2, 'Data platform', 'pending', { description: 'warehouse migration' }),
	project(3, 'Old job', 'completed')
];

beforeEach(() => {
	setPage({ params: { org: 'acme' } });
	session.setMe({ user: { id: 1, email: 'o@example.com', name: 'Owner' }, orgs: [{ slug: 'acme', name: 'Acme', role: 'owner', perms: ['timesheets_read', 'timesheets_admin'] }] });
	session.setOrg('acme');

	apiMock.mockImplementation((_m: string, p: string) =>
		Promise.resolve(p.endsWith('/projects') ? projects : [{ id: 9, name: 'Rizzware', kind: 'client' }]));
});

function names() {
	return screen.queryAllByRole('link').map((a) => a.textContent);
}

describe('projects list', () => {
	it('shows active projects by default with customer names', async () => {
		render(Page);
		await screen.findByText('Website');
		expect(names()).toEqual(['Website', 'Data platform']);
		expect(screen.getAllByText('Rizzware')).toHaveLength(2);
	});

	it('filters by status and searches name, customer and description', async () => {
		render(Page);
		await screen.findByText('Website');
		await fireEvent.change(screen.getByLabelText('Status filter'), { target: { value: 'all' } });
		expect(names()).toEqual(['Website', 'Data platform', 'Old job']);
		await fireEvent.change(screen.getByLabelText('Status filter'), { target: { value: 'completed' } });
		expect(names()).toEqual(['Old job']);
		await fireEvent.change(screen.getByLabelText('Status filter'), { target: { value: 'all' } });
		await fireEvent.input(screen.getByLabelText('Search projects'), { target: { value: 'WAREHOUSE' } });
		expect(names()).toEqual(['Data platform']);
		await fireEvent.input(screen.getByLabelText('Search projects'), { target: { value: 'rizz' } });
		expect(names()).toHaveLength(3);
	});

	it('still lists projects when contacts are forbidden', async () => {
		apiMock.mockImplementation((_m: string, p: string) =>
			p.endsWith('/projects') ? Promise.resolve(projects) : Promise.reject(new Error('forbidden')));
		render(Page);
		await screen.findByText('Website');
		expect(screen.queryByText('Rizzware')).toBeNull();
	});
});
