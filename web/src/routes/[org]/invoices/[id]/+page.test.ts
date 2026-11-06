import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

vi.mock('$app/state', () => ({
	page: { params: { org: 'acme', id: '7' }, url: new URL('http://localhost/acme/invoices/7') }
}));

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../../lib/api', () => ({
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
import { ApiError } from '../../../../lib/api';

// jsdom doesn't implement <dialog>'s showModal/close.
beforeEach(() => {
	HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
		this.setAttribute('open', '');
	});
	HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
		this.removeAttribute('open');
	});
	apiMock.mockReset();
});

const openInvoice = {
	id: 7,
	contact_name: 'Jane Client',
	number: 3,
	status: 'open',
	issue_date: '2026-11-01',
	due_date: '2026-11-15',
	subtotal: 10000,
	gst_total: 0,
	total: 10000,
	amount_paid: 0,
	outstanding: 10000,
	pay_options: [],
	lines: [
		{
			id: 1,
			description: 'Consulting',
			quantity_milli: 1000,
			unit_amount: 10000,
			gst_code: 'N-T',
			gst_amount: 0,
			amount: 10000
		}
	],
	payments: [],
	provider_url: ''
};

describe('invoice detail page - record payment failure', () => {
	it('keeps the dialog open and shows the server error verbatim on a failed payment', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'GET') return Promise.resolve(openInvoice);
			if (method === 'POST' && path.endsWith('/payments')) {
				return Promise.reject(
					new ApiError(409, 'invoice has payments; record a credit note instead')
				);
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		await screen.findByText('Jane Client');

		await fireEvent.click(screen.getByRole('button', { name: 'Record payment' }));
		// The dialog's own submit button has the same accessible name - the
		// trigger is now in the background, so .last() is the dialog's.
		const submitButtons = screen.getAllByRole('button', { name: 'Record payment' });
		await fireEvent.click(submitButtons[submitButtons.length - 1]);

		const errorText = await screen.findByText(
			'invoice has payments; record a credit note instead'
		);
		expect(errorText).toBeTruthy();

		// The dialog must still be open - the amount field (only rendered
		// inside it) is still present and interactable.
		expect(screen.getByLabelText('Amount ($)')).toBeTruthy();
	});
});
