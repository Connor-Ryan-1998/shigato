import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, setPage } from '../lib/test/app.svelte';
import { jsonResponse } from '../lib/test/mock-api';
// Exercises the real api client against a stubbed fetch.
vi.unmock('../lib/api');
import { createRawSnippet } from 'svelte';
import { render, screen, waitFor } from '@testing-library/svelte';

import Layout from './+layout.svelte';
import { session } from '../lib/session.svelte';

function childSnippet() {
	return createRawSnippet(() => ({ render: () => '<p data-testid="child"></p>' }));
}

beforeEach(() => setPage({ url: new URL('http://localhost/acme'), params: {} }));

describe('root layout', () => {
	const realLocation = window.location;

	beforeEach(() => {
		page.url = new URL('http://localhost/acme');
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		Object.defineProperty(window, 'location', { value: realLocation, writable: true });
	});

	// Ruling: a 401 before the session has ever loaded (an already-expired
	// session on a cold page load, where /me itself 401s) must fall back to
	// api.ts's plain redirect, never open ReloginDialog pre-filled with no
	// email - the relogin prompt is only registered once session.setMe has
	// actually run.
	it('redirects on a cold-load 401 instead of opening the relogin dialog with no email', async () => {
		const assign = vi.fn();
		Object.defineProperty(window, 'location', {
			value: { pathname: '/acme', search: '', assign },
			writable: true
		});
		vi.stubGlobal(
			'fetch',
			vi.fn().mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }))
		);

		render(Layout, { children: childSnippet() });

		await waitFor(() => expect(assign).toHaveBeenCalledWith('/login?next=%2Facme'));

		// Dialog.svelte always renders its own <h2>title</h2>, open or not,
		// so "Session expired" text existing in the DOM isn't proof the
		// dialog ever opened - only calling showModal() is.
		expect(HTMLDialogElement.prototype.showModal).not.toHaveBeenCalled();
	});

	// Item 1 (critical): /invite/<token> must stay reachable by a brand-new,
	// anonymous visitor. The layout's own /me probe 401s for them (no
	// session cookie at all); that must render the invite page's anonymous
	// accept form rather than bouncing to /login - otherwise the accept flow
	// is unreachable for exactly the people it exists for.
	it('renders the page anonymously under /invite/ instead of redirecting on a 401', async () => {
		const assign = vi.fn();
		Object.defineProperty(window, 'location', {
			value: { pathname: '/invite/some-token', search: '', assign },
			writable: true
		});
		page.url = new URL('http://localhost/invite/some-token');
		vi.stubGlobal(
			'fetch',
			vi.fn((path: RequestInfo | URL) => {
				const url = typeof path === 'string' ? path : path.toString();
				if (url.includes('/meta')) {
					return Promise.resolve(jsonResponse(200, { modules: [], sso: false }));
				}
				return Promise.resolve(jsonResponse(401, { error: 'unauthorized' }));
			})
		);

		render(Layout, { children: childSnippet() });

		await waitFor(() => expect(screen.getByTestId('child')).toBeTruthy());
		expect(assign).not.toHaveBeenCalled();
		expect(session.me).toBeNull();
	});

	it('registers the relogin prompt once the session loads, so a later 401 can open the dialog', async () => {

		const me = { user: { id: 1, email: 'staffer@example.com', name: 'Staffer' }, orgs: [] };
		vi.stubGlobal(
			'fetch',
			vi.fn((path: RequestInfo | URL) => {
				const url = typeof path === 'string' ? path : path.toString();
				if (url.includes('/me')) return Promise.resolve(jsonResponse(200, me));
				if (url.includes('/meta')) return Promise.resolve(jsonResponse(200, { modules: [] }));
				return Promise.resolve(jsonResponse(401, { error: 'unauthorized' }));
			})
		);

		render(Layout, { children: childSnippet() });

		await waitFor(() => expect(screen.getByTestId('child')).toBeTruthy());

		// A later 401 (e.g. a real session expiry) now has a real email to
		// pre-fill, from the session the layout just loaded.
		const { api } = await import('../lib/api');
		void api('GET', '/api/v1/something').catch(() => {});

		await waitFor(() =>
			expect((screen.getByLabelText('Email') as HTMLInputElement).value).toBe(
				'staffer@example.com'
			)
		);
	});
});
