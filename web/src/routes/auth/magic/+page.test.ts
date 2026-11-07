import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

const page = { url: new URL('http://localhost/auth/magic?token=abc123') };
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));
const { goto } = vi.hoisted(() => ({ goto: vi.fn() }));
vi.mock('$app/navigation', () => ({ goto }));

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../lib/api', () => ({
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
import { ApiError } from '../../../lib/api';

describe('magic sign-in page', () => {
	beforeEach(() => {
		apiMock.mockReset();
		goto.mockClear();
		page.url = new URL('http://localhost/auth/magic?token=abc123');
	});

	it('shows the fixed expired message on a 410', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'POST' && path === '/api/v1/auth/magic/verify') {
				return Promise.reject(new ApiError(410, 'This link has expired — request a new one'));
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		expect(await screen.findByText('This link has expired — request a new one')).toBeTruthy();
		expect(goto).not.toHaveBeenCalled();
	});

	it('loads the session and redirects on success', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'POST' && path === '/api/v1/auth/magic/verify') {
				return Promise.resolve({ redirect: '/portal' });
			}
			if (method === 'GET' && path === '/api/v1/me') {
				return Promise.resolve({
					user: { id: 1, email: 'tenant@example.com', name: 'Tenant' },
					orgs: [{ slug: 'acme', name: 'Acme', role: 'tenant', perms: [], portal: true }]
				});
			}
			if (method === 'GET' && path === '/api/v1/meta') {
				return Promise.resolve({ modules: [], sso: false });
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		await vi.waitFor(() => expect(goto).toHaveBeenCalledWith('/portal'));
	});
});
