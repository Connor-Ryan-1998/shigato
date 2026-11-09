import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import LineEditor from './LineEditor.svelte';

describe('LineEditor', () => {
	it('locks fields and add/remove controls while saving', () => {
		const props = { onSave: vi.fn(), busy: true };
		render(LineEditor, props);
		expect(screen.getByLabelText('Description').matches(':disabled')).toBe(true);
		expect(screen.getByRole('button', { name: 'Add line' }).matches(':disabled')).toBe(true);
	});
	it('starts with one row and lets you add and remove lines', async () => {
		render(LineEditor, { props: { onSave: vi.fn() } });

		expect(screen.getAllByLabelText('Description')).toHaveLength(1);

		await fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
		expect(screen.getAllByLabelText('Description')).toHaveLength(2);

		await fireEvent.click(screen.getAllByRole('button', { name: /Remove/ })[0]);
		expect(screen.getAllByLabelText('Description')).toHaveLength(1);
	});

	it('shows an inline error for an invalid amount', async () => {
		render(LineEditor, { props: { onSave: vi.fn() } });

		await fireEvent.input(screen.getByLabelText('Unit price ($)'), {
			target: { value: '1.234' }
		});

		expect(await screen.findByText(/Enter a valid amount/)).toBeTruthy();
	});

	it('disables Save while any line is invalid', async () => {
		render(LineEditor, { props: { onSave: vi.fn() } });

		const saveButton = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement;
		// A fresh row is incomplete (no description/quantity/amount yet).
		expect(saveButton.disabled).toBe(true);

		await fireEvent.input(screen.getByLabelText('Description'), {
			target: { value: 'Consulting' }
		});
		await fireEvent.input(screen.getByLabelText('Quantity'), { target: { value: '1' } });
		await fireEvent.input(screen.getByLabelText('Unit price ($)'), {
			target: { value: '100.00' }
		});
		expect(saveButton.disabled).toBe(false);

		await fireEvent.input(screen.getByLabelText('Unit price ($)'), {
			target: { value: '1.234' }
		});
		expect(saveButton.disabled).toBe(true);
	});

	it('calls onSave with parsed quantity_milli and unit_amount', async () => {
		const onSave = vi.fn();
		render(LineEditor, { props: { onSave } });

		await fireEvent.input(screen.getByLabelText('Description'), {
			target: { value: 'Consulting' }
		});
		await fireEvent.input(screen.getByLabelText('Quantity'), { target: { value: '1.5' } });
		await fireEvent.input(screen.getByLabelText('Unit price ($)'), {
			target: { value: '100.00' }
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));

		expect(onSave).toHaveBeenCalledWith([
			{ description: 'Consulting', quantity_milli: 1500, unit_amount: 10000, gst_code: 'GST' }
		]);
	});
});
