import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ExpenseForm, { type Category } from './ExpenseForm.svelte';

const categories: Category[] = [
	{ id: 1, name: 'Repairs', type: 'expense' },
	{ id: 2, name: 'Supplies', type: 'expense' }
];

async function fillRow(index: number, amount: string) {
	const amountInputs = screen.getAllByLabelText('Amount ($)') as HTMLInputElement[];
	await fireEvent.input(amountInputs[index], { target: { value: amount } });
}

describe('ExpenseForm', () => {
	it('enables Save when split lines sum to the bank line amount', async () => {
		const onSubmit = vi.fn();
		render(ExpenseForm, {
			props: { lineAmountCents: -16500, lineDate: '2026-11-01', categories, onSubmit }
		});

		await fireEvent.input(screen.getByLabelText('Description'), {
			target: { value: 'Plumber callout' }
		});
		await fillRow(0, '110.00');

		await fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
		await fillRow(1, '55.00');

		const save = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement;
		expect(save.disabled).toBe(false);

		await fireEvent.click(save);
		expect(onSubmit).toHaveBeenCalledOnce();
		const expense = onSubmit.mock.calls[0][0];
		expect(expense.lines).toHaveLength(2);
		expect(expense.lines[0].amount).toBe(11000);
		expect(expense.lines[1].amount).toBe(5500);
	});

	it('shows the mismatch message and disables Save when lines do not sum to the bank line amount', async () => {
		render(ExpenseForm, {
			props: {
				lineAmountCents: -16500,
				lineDate: '2026-11-01',
				categories,
				onSubmit: vi.fn()
			}
		});

		await fireEvent.input(screen.getByLabelText('Description'), {
			target: { value: 'Plumber callout' }
		});
		await fillRow(0, '110.00');
		await fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
		await fillRow(1, '54.00');

		expect(
			screen.getByText('Lines total $164.00 but the bank line is $165.00')
		).toBeTruthy();
		expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true);
	});

	it('updates the total when a line is removed', async () => {
		render(ExpenseForm, {
			props: {
				lineAmountCents: -16500,
				lineDate: '2026-11-01',
				categories,
				onSubmit: vi.fn()
			}
		});

		await fireEvent.input(screen.getByLabelText('Description'), {
			target: { value: 'Plumber callout' }
		});
		await fillRow(0, '110.00');
		await fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
		await fillRow(1, '54.00');

		expect(screen.getByText(/Lines total \$164\.00/)).toBeTruthy();

		const removeButtons = screen.getAllByRole('button', { name: /^Remove/ });
		await fireEvent.click(removeButtons[1]);

		expect(screen.getByText(/Lines total \$110\.00/)).toBeTruthy();
	});
});
