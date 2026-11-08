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
	it('saves positive standalone splits without a bank amount', async () => {
		const onSubmit = vi.fn();
		render(ExpenseForm, { props: { lineDate: '2026-11-01', categories, onSubmit } });
		await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'Cash supplies' } });
		await fillRow(0, '12.34');
		await fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
		await fillRow(1, '5.66');
		expect(screen.getByText('Lines total $18.00')).toBeTruthy();
		expect(screen.queryByText(/but the bank line/)).toBeNull();
		expect(screen.getByRole('button', { name: 'Save' })).toHaveProperty('disabled', false);
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(onSubmit).toHaveBeenCalledWith({ spent_on: '2026-11-01', description: 'Cash supplies', lines: [{ category_id: 1, amount: 1234 }, { category_id: 1, amount: 566 }] });
	});

	it.each(['', '0', '-1', '1.234', 'bad'])('rejects invalid standalone amount %s', async (amount) => {
		render(ExpenseForm, { props: { lineDate: '2026-11-01', categories, onSubmit: vi.fn() } });
		await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'Supplies' } });
		await fillRow(0, amount);
		expect(screen.getByRole('button', { name: 'Save' })).toHaveProperty('disabled', true);
	});

	it('locks draft controls while saving', () => {
		render(ExpenseForm, { props: { lineAmountCents: -1000, lineDate: '2026-11-01', categories, busy: true, onSubmit: vi.fn() } });
		expect(screen.getByLabelText('Description').matches(':disabled')).toBe(true);
		expect(screen.getByRole('button', { name: 'Add line' }).matches(':disabled')).toBe(true);
		expect(screen.getByLabelText('Receipt').matches(':disabled')).toBe(true);
	});

	it('locks draft during receipt upload and reports failed upload without attaching old file', async () => {
		let fail!: (error: Error) => void;
		const upload = vi.fn().mockResolvedValueOnce(5).mockImplementationOnce(() => new Promise((_resolve, reject) => { fail = reject; }));
		const onSubmit = vi.fn();
		render(ExpenseForm, { props: { lineAmountCents: -1000, lineDate: '2026-11-01', categories, onReceiptUpload: upload, onSubmit } });
		await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'Supplies' } });
		await fillRow(0, '10');
		await fireEvent.change(screen.getByLabelText('Receipt'), { target: { files: [new File(['one'], 'one.pdf', { type: 'application/pdf' })] } });
		await screen.findByText('Receipt attached.');
		await fireEvent.change(screen.getByLabelText('Receipt'), { target: { files: [new File(['two'], 'two.pdf', { type: 'application/pdf' })] } });
		expect(screen.getByLabelText('Description').matches(':disabled')).toBe(true);
		expect(screen.getByRole('button', { name: 'Save' })).toHaveProperty('disabled', true);
		fail(new Error('Upload failed'));
		await screen.findByText('Upload failed');
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(onSubmit.mock.calls[0][0].receipt_file_id).toBeUndefined();
	});
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
