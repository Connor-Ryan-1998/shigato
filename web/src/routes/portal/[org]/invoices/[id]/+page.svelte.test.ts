import { describe, expect, it, vi, beforeEach } from 'vitest';
import { flushSync } from 'svelte';
import { render, screen } from '@testing-library/svelte';

// A reactive stand-in for $app/state's page (runes work here because this
// is a .svelte.test.ts file), so the stale-response test can switch id the
// way an id-to-id hop does: same page instance, new page.params.id.
const page = $state({
	params: { org: 'acme', id: '7' },
	url: new URL('http://localhost/portal/acme/invoices/7')
});
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../../../lib/api', () => ({
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

beforeEach(() => {
	apiMock.mockReset();
	page.params = { org: 'acme', id: '7' };
});

const baseInvoice = {
	id: 7,
	number: 3,
	status: 'open',
	issue_date: '2026-11-01',
	due_date: '2026-11-15',
	total: 10000,
	amount_paid: 0,
	outstanding: 10000,
	payment_reference: 'REF-ABC123',
	pay_options: [],
	payments: [],
	provider_url: ''
};

describe('portal invoice detail page', () => {
	it('hides "Pay by card" when there is no provider_url', async () => {
		apiMock.mockResolvedValue(baseInvoice);

		render(Page);

		await screen.findByText('REF-ABC123');
		expect(screen.queryByRole('link', { name: 'Pay by card' })).toBeNull();
	});

	it('shows "Pay by card" when provider_url is set', async () => {
		apiMock.mockResolvedValue({ ...baseInvoice, provider_url: 'https://pay.example/inv/7' });

		render(Page);

		const link = (await screen.findByRole('link', { name: 'Pay by card' })) as HTMLAnchorElement;
		expect(link.getAttribute('href')).toBe('https://pay.example/inv/7');
	});

	it('shows the payment reference prominently', async () => {
		apiMock.mockResolvedValue(baseInvoice);

		render(Page);

		expect(await screen.findByText('REF-ABC123')).toBeTruthy();
	});

	it('discards an older response that resolves after a newer one (id-to-id hop)', async () => {
		let resolveFirst: (v: unknown) => void = () => {};
		const firstPending = new Promise((resolve) => {
			resolveFirst = resolve;
		});

		apiMock.mockImplementation((_method: string, path: string) => {
			if (path.endsWith('/invoices/7')) return firstPending;
			if (path.endsWith('/invoices/8')) {
				return Promise.resolve({ ...baseInvoice, id: 8, payment_reference: 'REF-NEWER' });
			}
			return Promise.reject(new Error(`unexpected call ${path}`));
		});

		render(Page);

		// Hop to invoice 8 before invoice 7's (slower) request resolves.
		page.params = { org: 'acme', id: '8' };
		flushSync();

		await screen.findByText('REF-NEWER');

		// The stale invoice-7 response lands after invoice 8's has already
		// rendered - it must never overwrite it.
		resolveFirst(baseInvoice);
		await Promise.resolve();
		await Promise.resolve();

		expect(screen.getByText('REF-NEWER')).toBeTruthy();
		expect(screen.queryByText('REF-ABC123')).toBeNull();
	});
});
