import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// $app/state's `page` is only read once (page.params.org) inside the load
// function - a plain stub is enough, no need for the real reactive store.
vi.mock('$app/state', () => ({
	page: { params: { org: 'acme' } }
}));

// vi.mock factories are hoisted above imports, so the mock fn has to be
// created via vi.hoisted rather than closed over directly.
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../lib/api', () => ({
	api: apiMock,
	ApiError: class ApiError extends Error {
		status: number;
		constructor(status: number, message: string) {
			super(message);
			this.name = 'ApiError';
			this.status = status;
		}
	}
}));

import Page from './+page.svelte';

// These exercise the exact branch the empty-org regression lived in:
// +page.svelte's own `isEmpty` check against the real GET /dashboard
// response shape, with `api` mocked so no network/backend is needed.
describe('[org] dashboard page empty/settled state', () => {
	beforeEach(() => {
		apiMock.mockReset();
	});

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
});
