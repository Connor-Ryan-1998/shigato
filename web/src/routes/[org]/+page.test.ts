import { beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../../lib/test/app.svelte';
import { apiMock } from '../../lib/test/mock-api';
import { render, screen } from '@testing-library/svelte';

import Page from './+page.svelte';

// These exercise the exact branch the empty-org regression lived in:
// +page.svelte's own `isEmpty` check against the real GET /dashboard
// response shape, with `api` mocked so no network/backend is needed.
beforeEach(() => setPage({ params: { org: 'acme' } }));

describe('[org] dashboard page empty/settled state', () => {
	it('shows the empty-org state when invoice_count is 0', async () => {
		apiMock.mockResolvedValue({
			core: {
				open_count: 0,
				open_outstanding: 0,
				overdue_count: 0,
				overdue_outstanding: 0,
				draft_count: 0,
				invoice_count: 0
			},
			modules: {}
		});

		render(Page);

		expect(await screen.findByText('No invoices yet — create one')).toBeTruthy();
		expect(screen.queryByText('Open invoices')).toBeNull();
	});

	it('shows the tiles, not the empty state, for a settled org (invoice history, nothing open)', async () => {
		apiMock.mockResolvedValue({
			core: {
				open_count: 0,
				open_outstanding: 0,
				overdue_count: 0,
				overdue_outstanding: 0,
				draft_count: 0,
				invoice_count: 3
			},
			modules: {}
		});

		render(Page);

		expect(await screen.findByText('Open invoices')).toBeTruthy();
		expect(screen.queryByText('No invoices yet — create one')).toBeNull();
	});

	it('links the reconciliation card to the org-scoped reconciliation screen', async () => {
		apiMock.mockResolvedValue({
			core: {
				open_count: 1,
				open_outstanding: 100,
				overdue_count: 0,
				overdue_outstanding: 0,
				draft_count: 0,
				invoice_count: 1
			},
			modules: {
				reconciliation: { unmatched_count: 2, oldest_unmatched_on: null, unmatched_in: 0, unmatched_out: 0 }
			}
		});

		render(Page);

		const card = await screen.findByRole('link', { name: /reconciliation/i });
		expect(card.getAttribute('href')).toBe('/acme/reconciliation');
		expect(screen.getByRole('link', { name: /open invoices/i }).getAttribute('href')).toBe(
			'/acme/invoices?status=open'
		);
	});
});
