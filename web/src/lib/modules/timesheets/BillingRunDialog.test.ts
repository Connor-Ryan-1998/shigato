import { describe, expect, it, beforeEach } from 'vitest';
import { render, screen, fireEvent, within, waitFor } from '@testing-library/svelte';
import { apiError, apiMock, mockApi } from '../../test/mock-api';
import { goto, setPage } from '../../test/app.svelte';
import { signIn } from '../../test/session';

import BillingRunDialog from './BillingRunDialog.svelte';

const CONTACTS = [
	{ id: 1, name: 'Tenant Tim', kind: 'tenant' },
	{ id: 2, name: 'Client Co', kind: 'client' },
	{ id: 3, name: 'Other Client', kind: 'client' }
];
const PREVIEW = {
	projects: [
		{
			project_id: 5,
			project_name: 'Website',
			minutes: 90,
			amount: 18000,
			gst_amount: 0,
			tasks: [
				{ task_id: 51, task_name: 'Design', minutes: 60, amount: 12000, gst_amount: 0 },
				{ task_id: 52, task_name: 'Build', minutes: 30, amount: 6000, gst_amount: 0 }
			]
		}
	],
	minutes: 90,
	amount: 18000,
	gst_amount: 0
};
const PREVIEW_URL = (client: number) => `GET /api/v1/orgs/acme/billing-runs/preview?client_contact_id=${client}&from=2026-11-01&to=2026-11-07`;
const RUN = 'POST /api/v1/orgs/acme/billing-runs';

function invoice(id: number, status = 'draft') {
	return { id, number: status === 'draft' ? null : id, status, contact_name: 'Client Co', total: 1000 };
}

function mount() {
	render(BillingRunDialog, { props: { open: true, org: 'acme', defaultFrom: '2026-11-01', defaultTo: '2026-11-07' } });
}

const dialog = () => within(screen.getByRole('dialog', { name: 'Billing run' }));

describe('billing run dialog', () => {
	beforeEach(() => {
		setPage({ params: { org: 'acme' }, url: 'http://localhost/acme/invoices' });
		signIn({ perms: ['invoices_write'] });
	});

	it('posts the selected tasks per project and opens the single invoice', async () => {
		mockApi({
			'GET /api/v1/orgs/acme/contacts': CONTACTS,
			[PREVIEW_URL(2)]: PREVIEW,
			[RUN]: { invoices: [{ invoice: invoice(77) }], entry_count: 2, total_minutes: 60 }
		});
		mount();
		await dialog().findByText('Design');
		expect(dialog().queryByRole('option', { name: 'Tenant Tim' })).toBeNull();
		await fireEvent.click(dialog().getByRole('checkbox', { name: /Build/ }));
		await fireEvent.click(dialog().getByLabelText('One invoice per project'));
		await fireEvent.click(dialog().getByRole('button', { name: 'Create invoices' }));
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/acme/invoices/77'));
		expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/billing-runs', {
			client_contact_id: 2,
			from: '2026-11-01',
			to: '2026-11-07',
			due_in_days: 14,
			issue: false,
			split: 'project',
			tasks: [{ task_id: 51, minutes: 60, amount: 12000 }]
		});
	});

	it('lists every invoice and its issue error when a run makes several', async () => {
		mockApi({
			'GET /api/v1/orgs/acme/contacts': CONTACTS,
			[PREVIEW_URL(2)]: PREVIEW,
			[RUN]: {
				invoices: [{ invoice: invoice(77, 'open') }, { invoice: invoice(78), issue_error: 'invalid state' }],
				entry_count: 2,
				total_minutes: 90
			}
		});
		mount();
		await dialog().findByText('Design');
		await fireEvent.click(dialog().getByRole('button', { name: 'Create invoices' }));
		const first = await dialog().findByRole('link', { name: /Invoice #77/ });
		expect(first.getAttribute('href')).toBe('/acme/invoices/77');
		expect(dialog().getByRole('link', { name: /Draft invoice/ }).getAttribute('href')).toBe('/acme/invoices/78');
		expect(dialog().getByText(/invalid state/)).toBeTruthy();
		expect(goto).not.toHaveBeenCalled();
	});

	it('keeps the selection on a stale preview and refreshes on request', async () => {
		let previews = 0;
		mockApi({
			'GET /api/v1/orgs/acme/contacts': CONTACTS,
			[PREVIEW_URL(2)]: () => {
				previews++;
				return PREVIEW;
			},
			[RUN]: apiError(409, 'billing preview changed; refresh and review')
		});
		mount();
		await dialog().findByText('Design');
		await fireEvent.click(dialog().getByRole('checkbox', { name: /Build/ }));
		await fireEvent.click(dialog().getByRole('button', { name: 'Create invoices' }));
		expect(await dialog().findByText('billing preview changed; refresh and review')).toBeTruthy();
		expect((dialog().getByRole('checkbox', { name: /Build/ }) as HTMLInputElement).checked).toBe(false);
		await fireEvent.click(dialog().getByRole('button', { name: 'Refresh preview' }));
		await waitFor(() => expect(previews).toBe(2));
		expect((dialog().getByRole('checkbox', { name: /Build/ }) as HTMLInputElement).checked).toBe(false);
		expect((dialog().getByRole('checkbox', { name: /Design/ }) as HTMLInputElement).checked).toBe(true);
	});

	it('reloads the preview when the client changes', async () => {
		mockApi({
			'GET /api/v1/orgs/acme/contacts': CONTACTS,
			[PREVIEW_URL(2)]: PREVIEW,
			[PREVIEW_URL(3)]: { projects: [], minutes: 0, amount: 0, gst_amount: 0 }
		});
		mount();
		await dialog().findByText('Design');
		await fireEvent.change(dialog().getByLabelText('Client'), { target: { value: '3' } });
		expect(await dialog().findByText('No unbilled time for this client in this period.')).toBeTruthy();
		expect(dialog().queryByText('Design')).toBeNull();
		expect((dialog().getByRole('button', { name: 'Create invoices' }) as HTMLButtonElement).disabled).toBe(true);
	});

	it('search hides rows without dropping their selection', async () => {
		mockApi({ 'GET /api/v1/orgs/acme/contacts': CONTACTS, [PREVIEW_URL(2)]: PREVIEW });
		mount();
		await dialog().findByText('Design');
		await fireEvent.input(dialog().getByLabelText('Search projects and tasks'), { target: { value: 'build' } });
		expect(dialog().queryByText('Design')).toBeNull();
		expect(dialog().getByText(/Selected: 1h 30m/)).toBeTruthy();
	});
});
