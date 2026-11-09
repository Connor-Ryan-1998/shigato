import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

vi.mock('$app/state', () => ({
	page: { params: { org: 'acme' }, url: new URL('http://localhost/acme/invoices/new') }
}));

const { gotoMock } = vi.hoisted(() => ({ gotoMock: vi.fn() }));
vi.mock('$app/navigation', () => ({
	goto: gotoMock,
	beforeNavigate: vi.fn()
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
	gotoMock.mockReset();
});

describe('new invoice page - New contact dialog failure', () => {
	it.each(['pending', 'rejected'])('retains a saved invoice without allowing another write when navigation is %s', async (navigation) => {
		apiMock.mockImplementation((method: string) => Promise.resolve(method === 'GET' ? [{ id: 4, name: 'Client', kind: 'client' }] : { id: 77 }));
		gotoMock.mockImplementation(() => navigation === 'pending' ? new Promise(() => {}) : Promise.reject(new Error('Navigation failed')));
		render(Page);
		await screen.findByRole('option', { name: 'Client' });
		await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'Work' } });
		await fireEvent.input(screen.getByLabelText('Quantity'), { target: { value: '1' } });
		await fireEvent.input(screen.getByLabelText('Unit price ($)'), { target: { value: '100' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect((await screen.findByRole('link', { name: 'View saved invoice' })).getAttribute('href')).toBe('/acme/invoices/77');
		expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
		expect(apiMock.mock.calls.filter(([method]) => method === 'POST')).toHaveLength(1);
	});
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
		await fireEvent.click(screen.getByRole('button', { name: 'Create contact' }));

		const errorText = await screen.findByText('name is required');
		expect(errorText).toBeTruthy();

		// The dialog must still be open - its fields are still present.
		expect(screen.getByLabelText('Name')).toBeTruthy();
		expect(screen.getByLabelText('Contact kind')).toBeTruthy();
	});
});
