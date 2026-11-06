import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';

const page = { url: new URL('http://localhost/login') };
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

import Login from './+page.svelte';
import { session } from '../../lib/session.svelte';

function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('login page', () => {
	beforeEach(() => {
		session.clear();
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it('shows no SSO button and no error banner by default', async () => {
		page.url = new URL('http://localhost/login');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { modules: [], sso: false })));

		render(Login);

		expect(screen.queryByRole('link', { name: /sign in with sso/i })).toBeNull();
		expect(screen.queryByRole('alert')).toBeNull();
	});

	it('shows a "Sign in with SSO" link to the start endpoint when sso is enabled', async () => {
		page.url = new URL('http://localhost/login?next=%2Facme');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { modules: [], sso: true })));

		render(Login);

		const link = (await screen.findByRole('link', {
			name: /sign in with sso/i
		})) as HTMLAnchorElement;
		expect(link.getAttribute('href')).toBe('/api/v1/auth/oidc/start?next=%2Facme');
	});

	it('shows the fixed message for a known error code', async () => {
		page.url = new URL('http://localhost/login?error=not_invited');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { modules: [], sso: true })));

		render(Login);

		expect(
			await screen.findByText('No shigoto account for this email — ask an owner to invite you.')
		).toBeTruthy();
	});

	it('ignores an unknown error code', async () => {
		page.url = new URL('http://localhost/login?error=something_weird');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { modules: [], sso: false })));

		render(Login);

		expect(screen.queryByRole('alert')).toBeNull();
	});
});
