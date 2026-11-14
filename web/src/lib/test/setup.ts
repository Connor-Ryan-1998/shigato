// setup runs before every Vitest file (vite.config.ts setupFiles). It
// installs the shared mocks so test files don't each repeat them:
//   - lib/api: api() goes through apiMock (mock-api.ts); every other export,
//     including ApiError, is the real one. Files that test the real client
//     opt out with vi.unmock('<relative path to lib/api>').
//   - $app/state and $app/navigation: backed by app.svelte.ts.
//   - <dialog>: jsdom has no showModal/close.
// Before each test the api routes, route params, navigation spies and the
// session store reset, and window.confirm returns true.
// Paths are relative: the $lib alias doesn't resolve in setup files.

import { afterEach, beforeEach, vi } from 'vitest';
import { assertAllRouted, resetApiMock } from './mock-api';
import { resetApp } from './app.svelte';
import { session } from '../session.svelte';

vi.mock('../api', async (importOriginal) => {
	const real = await importOriginal<typeof import('../api')>();
	const { apiMock } = await import('./api-spy');
	return { ...real, api: (...args: Parameters<typeof real.api>) => apiMock(...args) };
});

vi.mock('$app/state', async () => {
	const app = await import('./app.svelte');
	return { get page() { return app.page; } };
});

vi.mock('$app/navigation', async () => {
	const app = await import('./app.svelte');
	return {
		goto: app.goto,
		beforeNavigate: app.beforeNavigate,
		afterNavigate: app.afterNavigate,
		invalidate: app.invalidate,
		invalidateAll: app.invalidateAll,
		replaceState: app.replaceState,
		pushState: app.pushState
	};
});

// Spies, so a test can assert a dialog was opened or closed.
const showModal = vi.fn(function (this: HTMLDialogElement) {
	this.open = true;
});
// Like the browser, closing an open dialog fires its close event.
const close = vi.fn(function (this: HTMLDialogElement) {
	if (!this.open) return;
	this.open = false;
	this.dispatchEvent(new Event('close'));
});
HTMLDialogElement.prototype.showModal = showModal;
HTMLDialogElement.prototype.close = close;

beforeEach(() => {
	showModal.mockClear();
	close.mockClear();
	resetApiMock();
	resetApp();
	session.clear();
	vi.spyOn(window, 'confirm').mockReturnValue(true);
});

// An api() call no route answered fails the test even when the component
// swallowed the error.
afterEach(() => assertAllRouted());
