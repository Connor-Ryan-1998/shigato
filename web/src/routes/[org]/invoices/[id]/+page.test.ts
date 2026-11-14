import { describe, expect, it, beforeEach } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import { apiMock, mockApi } from '../../../../lib/test/mock-api';
import { setPage } from '../../../../lib/test/app.svelte';
import { signIn } from '../../../../lib/test/session';

import Page from './+page.svelte';

const BASE = '/api/v1/orgs/acme/invoices/7';

function invoice(overrides: Record<string, unknown> = {}) {
	return {
		id: 7,
		contact_name: 'Jane Client',
		number: 3,
		status: 'open',
		written_off: false,
		issue_date: '2026-11-01',
		due_date: '2026-11-15',
		subtotal: 10000,
		gst_total: 0,
		total: 10000,
		amount_paid: 0,
		outstanding: 10000,
		pay_options: [],
		lines: [{ id: 1, description: 'Consulting', quantity_milli: 1000, unit_amount: 10000, gst_code: 'N-T', gst_amount: 0, amount: 10000 }],
		payments: [],
		provider_url: '',
		...overrides
	};
}

function actions() {
	return within(screen.getByRole('region', { name: 'Invoice actions' }));
}

describe('invoice detail status actions', () => {
	beforeEach(() => {
		setPage({ params: { org: 'acme', id: '7' }, url: 'http://localhost/acme/invoices/7' });
		signIn({ perms: ['invoices_read', 'invoices_write', 'payments_write'] });
	});

	it('offers write-off, not reinstate, on an open invoice', async () => {
		mockApi({ [`GET ${BASE}`]: invoice() });
		render(Page);
		await screen.findByText('Jane Client');
		expect(actions().getByRole('button', { name: 'Mark uncollectible' })).toBeTruthy();
		expect(actions().queryByRole('button', { name: 'Reinstate' })).toBeNull();
	});

	it('offers reinstate, payment and void on an unpaid uncollectible invoice', async () => {
		mockApi({ [`GET ${BASE}`]: invoice({ status: 'uncollectible', written_off: true, outstanding: 0 }) });
		render(Page);
		await screen.findByText('Jane Client');
		expect(actions().getByRole('button', { name: 'Reinstate' })).toBeTruthy();
		expect(actions().getByRole('button', { name: 'Record payment' })).toBeTruthy();
		expect(actions().getByRole('button', { name: 'Void' })).toBeTruthy();
		expect(actions().queryByRole('button', { name: 'Mark uncollectible' })).toBeNull();
	});

	it('hides void on a part-paid uncollectible invoice', async () => {
		mockApi({ [`GET ${BASE}`]: invoice({ status: 'uncollectible', written_off: true, amount_paid: 4000, outstanding: 0 }) });
		render(Page);
		await screen.findByText('Jane Client');
		expect(actions().queryByRole('button', { name: 'Void' })).toBeNull();
	});

	it('posts the write-off reason and refetches', async () => {
		let loads = 0;
		mockApi({
			[`GET ${BASE}`]: () => {
				loads++;
				return loads === 1 ? invoice() : invoice({ status: 'uncollectible', written_off: true, outstanding: 0 });
			},
			[`POST ${BASE}/uncollectible`]: invoice({ status: 'uncollectible', written_off: true })
		});
		render(Page);
		await screen.findByText('Jane Client');
		await fireEvent.click(actions().getByRole('button', { name: 'Mark uncollectible' }));
		const dialog = screen.getByRole('dialog', { name: 'Mark uncollectible' });
		await fireEvent.input(within(dialog).getByLabelText('Reason'), { target: { value: 'Client insolvent' } });
		await fireEvent.click(within(dialog).getByRole('button', { name: 'Mark uncollectible' }));
		expect(apiMock).toHaveBeenCalledWith('POST', `${BASE}/uncollectible`, { reason: 'Client insolvent' });
		expect(await actions().findByRole('button', { name: 'Reinstate' })).toBeTruthy();
		expect(loads).toBe(2);
	});

	it('warns that the Stripe link stays active when reinstating a sent invoice', async () => {
		mockApi({ [`GET ${BASE}`]: invoice({ status: 'uncollectible', written_off: true, outstanding: 0, provider_url: 'https://pay.example/in_1' }) });
		render(Page);
		await screen.findByText('Jane Client');
		await fireEvent.click(actions().getByRole('button', { name: 'Reinstate' }));
		const dialog = screen.getByRole('dialog', { name: 'Reinstate invoice' });
		expect(within(dialog).getByText(/The Stripe payment link stays active/)).toBeTruthy();
	});

	it('seeds a payment on an uncollectible invoice with its unpaid balance', async () => {
		mockApi({ [`GET ${BASE}`]: invoice({ status: 'uncollectible', written_off: true, amount_paid: 4000, outstanding: 0 }) });
		render(Page);
		await screen.findByText('Jane Client');
		await fireEvent.click(actions().getByRole('button', { name: 'Record payment' }));
		expect((screen.getByLabelText('Amount ($)') as HTMLInputElement).value).toBe('60.00');
	});
});
