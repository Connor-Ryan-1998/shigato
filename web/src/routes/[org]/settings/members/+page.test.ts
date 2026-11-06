import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

vi.mock('$app/state', () => ({
	page: { params: { org: 'acme' }, url: new URL('http://localhost/acme/settings/members') }
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

const MEMBERS = [
	{ user_id: 1, name: 'Owner One', email: 'owner@example.com', role: 'owner', sso_linked: false }
];

beforeEach(() => {
	HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
		this.setAttribute('open', '');
	});
	HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
		this.removeAttribute('open');
	});
	apiMock.mockReset();
});

describe('members page - role change', () => {
	it('sends a PUT with the new role when the select changes', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'GET' && path.endsWith('/members')) return Promise.resolve(MEMBERS);
			if (method === 'GET' && path.endsWith('/invites')) return Promise.resolve([]);
			if (method === 'PUT' && path.endsWith('/members/1')) return Promise.resolve(undefined);
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		await screen.findByText('owner@example.com');

		const select = screen.getByLabelText('Role for Owner One') as HTMLSelectElement;
		await fireEvent.change(select, { target: { value: 'staff' } });

		expect(apiMock).toHaveBeenCalledWith('PUT', '/api/v1/orgs/acme/members/1', { role: 'staff' });
	});

	it('shows a 409 inline and reverts the select when demoting the last owner fails', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'GET' && path.endsWith('/members')) return Promise.resolve(MEMBERS);
			if (method === 'GET' && path.endsWith('/invites')) return Promise.resolve([]);
			if (method === 'PUT' && path.endsWith('/members/1')) {
				return Promise.reject(new ApiError(409, 'an organisation needs at least one owner'));
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		await screen.findByText('owner@example.com');

		const select = screen.getByLabelText('Role for Owner One') as HTMLSelectElement;
		await fireEvent.change(select, { target: { value: 'staff' } });

		const errorText = await screen.findByText('an organisation needs at least one owner');
		expect(errorText).toBeTruthy();
		expect(select.value).toBe('owner');
	});
});

describe('members page - revoke invite', () => {
	it('shows a revoke failure inline without clearing the invite list', async () => {
		const invites = [{ id: 7, email: 'pending@example.com', role: 'staff', expires_at: '2026-12-01T00:00:00Z' }];
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'GET' && path.endsWith('/members')) return Promise.resolve(MEMBERS);
			if (method === 'GET' && path.endsWith('/invites')) return Promise.resolve(invites);
			if (method === 'DELETE' && path.endsWith('/invites/7')) {
				return Promise.reject(new ApiError(404, 'not found'));
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		await screen.findByText('pending@example.com — staff');

		await fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));

		const errorText = await screen.findByText('not found');
		expect(errorText).toBeTruthy();
		// The invite is still shown - the failed revoke never removed it from
		// the still-mounted list.
		expect(screen.getByText('pending@example.com — staff')).toBeTruthy();
	});
});
