import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto, replaceState, setPage } from '../../../lib/test/app.svelte';
import { apiMock } from '../../../lib/test/mock-api';
import { render, screen } from '@testing-library/svelte';

import Page from './+page.svelte';
import { ApiError } from '../../../lib/api';

beforeEach(() => setPage({ url: new URL('http://localhost/auth/magic?token=abc123') }));

describe('magic sign-in page', () => {
	it('shows the fixed expired message on a 410, and scrubs the token from the URL', async () => {
		apiMock.mockImplementation((method: string, path: string) => {
			if (method === 'POST' && path === '/api/v1/auth/magic/verify') {
				return Promise.reject(new ApiError(410, 'This link has expired — request a new one'));
			}
			return Promise.reject(new Error(`unexpected call ${method} ${path}`));
		});

		render(Page);

		expect(await screen.findByText('This link has expired — request a new one')).toBeTruthy();
		expect(screen.getByRole('heading', { level: 1, name: 'Sign in' })).toBeTruthy();
		expect(goto).not.toHaveBeenCalled();
		// Scrubbed immediately on mount, before the verify call even
		// resolves - a used-up, now-useless token still shouldn't linger in
		// history/the referrer.
		expect(replaceState).toHaveBeenCalledWith('/auth/magic', {});
	});

	it('loads the session, scrubs the token from the URL, and redirects on success', async () => {
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

		// Scrubbed on mount, independent of the verify call's own outcome -
		// asserted before the redirect lands, not just after.
		expect(replaceState).toHaveBeenCalledWith('/auth/magic', {});

		await vi.waitFor(() =>
			expect(goto).toHaveBeenCalledWith('/portal', { replaceState: true })
		);
	});
});
