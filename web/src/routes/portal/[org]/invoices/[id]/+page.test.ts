import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

vi.mock('$app/state', () => ({
	page: { params: { org: 'acme', id: '7' }, url: new URL('http://localhost/portal/acme/invoices/7') }
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
});
