import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

vi.mock('$app/state', () => ({
	page: { params: { org: 'acme' }, url: new URL('http://localhost/acme/invoices/new') }
}));

vi.mock('$app/navigation', () => ({
	goto: vi.fn()
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

describe('new invoice page - New contact dialog failure', () => {
	it('keeps the dialog open and shows the server error verbatim when POST /contacts fails', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'GET' && path.endsWith('/contacts')) return Promise.resolve([]);
			if (method === 'POST' && path.endsWith('/contacts')) {
				return Promise.reject(new ApiError(422, 'name is required'));
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		await screen.findByRole('heading', { name: 'New invoice' });

		await fireEvent.click(screen.getByRole('button', { name: 'New contact' }));
		await screen.findByLabelText('Name');

		await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Jane Client' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Create' }));

		const errorText = await screen.findByText('name is required');
		expect(errorText).toBeTruthy();

		// The dialog must still be open - its fields are still present.
		expect(screen.getByLabelText('Name')).toBeTruthy();
		expect(screen.getByLabelText('Kind')).toBeTruthy();
	});
});
