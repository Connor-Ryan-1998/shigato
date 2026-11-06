import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../api', () => ({
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

import LineDrawer from './LineDrawer.svelte';
import type { BankLine } from './BankLineTable.svelte';
import type { Category } from './ExpenseForm.svelte';

// jsdom doesn't implement <dialog>'s showModal/close - Drawer.svelte (via
// Dialog's same trick) calls them directly.
beforeEach(() => {
	HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
		this.setAttribute('open', '');
	});
	HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
		this.removeAttribute('open');
	});
	apiMock.mockReset();
});

const categories: Category[] = [{ id: 1, name: 'Repairs', type: 'expense' }];

function debitLine(id: number, amount = -16500): BankLine {
	return {
		id,
		bank_account_id: 1,
		import_id: 1,
		posted_on: '2026-11-02',
		amount,
		description: 'PLUMBER CALLOUT',
		status: 'unmatched',
		match: null
	};
}

describe('LineDrawer - expense warnings', () => {
	it('shows server warnings after creating an expense, keeps the drawer open, and still refreshes the list', async () => {
		const onChanged = vi.fn();
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'GET' && path.includes('/suggestions')) return Promise.resolve([]);
			if (method === 'POST' && path.endsWith('/expense')) {
				return Promise.resolve({ warnings: ['GST estimated at the category default'] });
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(LineDrawer, {
			props: {
				open: true,
				org: 'acme',
				line: debitLine(1),
				categories,
				suppliers: [],
				onReceiptUpload: vi.fn(),
				onChanged
			}
		});

		await screen.findByText('No suggestions.');
		await fireEvent.click(screen.getByRole('button', { name: 'Create expense' }));

		await fireEvent.input(screen.getByLabelText('Description'), {
			target: { value: 'Plumber callout' }
		});
		await fireEvent.input(screen.getByLabelText('Amount ($)'), {
			target: { value: '165.00' }
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));

		expect(
			await screen.findByText('GST estimated at the category default')
		).toBeTruthy();
		expect(onChanged).toHaveBeenCalledOnce();
		// The drawer must stay open to show the warning - a close would have
		// called the (mocked) native dialog close().
		expect(HTMLDialogElement.prototype.close).not.toHaveBeenCalled();
	});
});

describe('LineDrawer - suggestions race', () => {
	it('ignores a suggestions response for a line that is no longer open', async () => {
		const deferred: Record<number, { resolve: (v: unknown[]) => void }> = {};
		apiMock.mockImplementation((method: string, path: string) => {
			const match = /bank-lines\/(\d+)\/suggestions/.exec(path);
			if (method === 'GET' && match) {
				const lineId = Number(match[1]);
				return new Promise((resolve) => {
					deferred[lineId] = { resolve };
				});
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		const { rerender } = render(LineDrawer, {
			props: {
				open: true,
				org: 'acme',
				line: debitLine(1),
				categories,
				suppliers: [],
				onReceiptUpload: vi.fn(),
				onChanged: vi.fn()
			}
		});

		await waitFor(() => expect(deferred[1]).toBeTruthy());

		// Switch to a second line before the first line's suggestions
		// request resolves.
		await rerender({ line: debitLine(2) });
		await waitFor(() => expect(deferred[2]).toBeTruthy());

		// Line 2's own suggestions arrive first.
		deferred[2].resolve([{ kind: 'expense', id: 99, score: 80, reason: 'amount and date match' }]);
		await screen.findByText(/expense — amount and date match/);

		// Line 1's stale response arrives late - it must not replace line 2's
		// suggestions (which would let Accept post a match for line 1 while
		// line 2 is the one actually open).
		deferred[1].resolve([{ kind: 'expense', id: 11, score: 50, reason: 'amount only' }]);
		await Promise.resolve();
		await Promise.resolve();

		expect(screen.getByText(/expense — amount and date match/)).toBeTruthy();
		expect(screen.queryByText(/amount only/)).toBeNull();
	});
});
