import { describe, expect, it, beforeEach, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { mockApi } from '../../../lib/test/mock-api';
import { setPage } from '../../../lib/test/app.svelte';
import { signIn } from '../../../lib/test/session';
import { session } from '../../../lib/session.svelte';

import Page from './+page.svelte';

const LIST = 'GET /api/v1/orgs/acme/invoices?limit=50&offset=0';

describe('invoices page billing run', () => {
	beforeEach(() => {
		setPage({ params: { org: 'acme' }, url: 'http://localhost/acme/invoices' });
		vi.useFakeTimers({ toFake: ['Date'] });
		vi.setSystemTime(new Date(2026, 10, 7));
	});

	it('opens the billing run for this month with timesheets and invoices_write', async () => {
		signIn({ perms: ['invoices_read', 'invoices_write'] });
		session.setModules(['timesheets']);
		mockApi({
			[LIST]: { invoices: [], total: 0 },
			'GET /api/v1/orgs/acme/contacts': [{ id: 2, name: 'Client Co', kind: 'client' }],
			'GET /api/v1/orgs/acme/billing-runs/preview?client_contact_id=2&from=2026-11-01&to=2026-11-07': { projects: [], minutes: 0, amount: 0, gst_amount: 0 }
		});
		render(Page);
		await fireEvent.click(await screen.findByRole('button', { name: 'Billing run' }));
		expect(await screen.findByText('No unbilled time for this client in this period.')).toBeTruthy();
	});

	it('hides the billing run without the timesheets module', async () => {
		signIn({ perms: ['invoices_read', 'invoices_write'] });
		session.setModules([]);
		mockApi({ [LIST]: { invoices: [], total: 0 } });
		render(Page);
		await screen.findByText('No invoices in this view');
		expect(screen.queryByRole('button', { name: 'Billing run' })).toBeNull();
	});

	it('hides the billing run without invoices_write', async () => {
		signIn({ perms: ['invoices_read'] });
		session.setModules(['timesheets']);
		mockApi({ [LIST]: { invoices: [], total: 0 } });
		render(Page);
		await screen.findByText('No invoices in this view');
		expect(screen.queryByRole('button', { name: 'Billing run' })).toBeNull();
	});
});
