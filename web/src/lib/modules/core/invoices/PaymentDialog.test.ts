import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import PaymentDialog from './PaymentDialog.svelte';

// jsdom doesn't implement <dialog>'s showModal/close - Dialog.svelte calls
// them directly, so stub them out rather than exercising the real modal
// behaviour (that's native browser behaviour, not this component's logic).
beforeEach(() => {
	HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
		this.setAttribute('open', '');
	});
	HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
		this.removeAttribute('open');
	});
});

describe('PaymentDialog', () => {
	it('opens prefilled with the outstanding balance and submits integer cents and the selected method', async () => {
		const onSubmit = vi.fn();
		render(PaymentDialog, { props: { open: true, outstandingCents: 12345, onSubmit } });

		const amountInput = screen.getByLabelText('Amount ($)') as HTMLInputElement;
		expect(amountInput.value).toBe('123.45');

		await fireEvent.change(screen.getByLabelText('Method'), { target: { value: 'cash' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Record payment' }));

		expect(onSubmit).toHaveBeenCalledOnce();
		const payment = onSubmit.mock.calls[0][0];
		expect(payment.amount).toBe(12345);
		expect(payment.method).toBe('cash');
		expect(typeof payment.paid_on).toBe('string');
	});

	it('shows a passed-in error message inside the dialog', () => {
		render(PaymentDialog, {
			props: {
				open: true,
				outstandingCents: 1000,
				error: 'invoice has payments; record a credit note instead',
				onSubmit: vi.fn()
			}
		});

		expect(
			screen.getByText('invoice has payments; record a credit note instead')
		).toBeTruthy();
	});
});
