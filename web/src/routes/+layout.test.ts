import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { render, screen, waitFor } from '@testing-library/svelte';

const page = { url: new URL('http://localhost/acme'), params: {} };
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));

import Layout from './+layout.svelte';
import { session } from '../lib/session.svelte';

function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

function childSnippet() {
	return createRawSnippet(() => ({ render: () => '<p data-testid="child"></p>' }));
}

describe('root layout', () => {
	const realLocation = window.location;

	beforeEach(() => {
		session.clear();
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
		// Dialog.svelte always renders its own <h2>title</h2>, open or not,
		// so "Session expired" text existing in the DOM isn't proof the
		// dialog ever opened - only calling showModal() is.
		const showModal = vi.fn();
		HTMLDialogElement.prototype.showModal = showModal;
		HTMLDialogElement.prototype.close = vi.fn();

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

		expect(showModal).not.toHaveBeenCalled();
	});

	it('registers the relogin prompt once the session loads, so a later 401 can open the dialog', async () => {
		HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
			this.setAttribute('open', '');
		});
		HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
			this.removeAttribute('open');
		});

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
