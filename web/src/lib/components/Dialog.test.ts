import { expect, it, vi } from 'vitest';
import { createRawSnippet, tick } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import Dialog from './Dialog.svelte';
import { session } from '../session.svelte';
import { api, registerReloginPrompt, resetOnUnauthorized } from '../api';
const { beforeNavigateMock } = vi.hoisted(() => ({ beforeNavigateMock: vi.fn() }));
vi.mock('$app/navigation', () => ({ beforeNavigate: beforeNavigateMock }));

HTMLDialogElement.prototype.showModal = function () { this.open = true; };
HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new Event('close')); };
const children = createRawSnippet(() => ({ render: () => '<label>Name<input name="name" value="Initial" /></label>' }));

it('keeps a dirty editor open when Escape discard is refused', async () => {
	vi.spyOn(window, 'confirm').mockReturnValue(false);
	render(Dialog, { title: 'Edit', open: true, children });
	await tick();
	await fireEvent.input(screen.getByRole('textbox'), { target: { value: 'Changed' } });
	const dialog = screen.getByRole('dialog');
	const event = new Event('cancel', { cancelable: true });
	dialog.dispatchEvent(event);
	expect(event.defaultPrevented).toBe(true);
	expect(window.confirm).toHaveBeenCalledWith('Discard unsaved changes?');
	expect(screen.getByRole('textbox')).toHaveProperty('value', 'Changed');
	vi.restoreAllMocks();
});

it('blocks Escape during a pending save without a discard prompt', async () => {
	const confirm = vi.spyOn(window, 'confirm');
	const props = { title: 'Edit', open: true, busy: true, children };
	render(Dialog, props);
	const event = new Event('cancel', { cancelable: true });
	screen.getByRole('dialog').dispatchEvent(event);
	expect(event.defaultPrevented).toBe(true);
	expect(confirm).not.toHaveBeenCalled();
	vi.restoreAllMocks();
});

it('protects navigation away from a dirty dialog', async () => {
	session.setMe({ user: { id: 1, name: 'Owner', email: 'owner@example.com' }, orgs: [] });
	vi.spyOn(window, 'confirm').mockReturnValue(false);
	render(Dialog, { title: 'Edit', open: true, children });
	await tick();
	await fireEvent.input(screen.getByRole('textbox'), { target: { value: 'Changed' } });
	const cancel = vi.fn();
	expect(beforeNavigateMock.mock.calls.length).toBeGreaterThan(0);
	beforeNavigateMock.mock.calls.at(-1)![0]({ to: { url: new URL('http://localhost/acme') }, cancel });
	expect(cancel).toHaveBeenCalledOnce();
	vi.restoreAllMocks();
	session.clear();
});

it('does not prompt beforeunload after expired-session authentication is cancelled', async () => {
	const realLocation = window.location;
	Object.defineProperty(window, 'location', { value: { pathname: '/acme/invoices/new', search: '', assign: vi.fn() }, writable: true });
	vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('{"error":"unauthorized"}', { status: 401 }))));
	resetOnUnauthorized();
	registerReloginPrompt(async () => false);
	session.setMe({ user: { id: 1, name: 'Owner', email: 'owner@example.com' }, orgs: [] });
	try {
		render(Dialog, { title: 'Edit', open: true, busy: true, children });
		const before = new Event('beforeunload', { cancelable: true });
		window.dispatchEvent(before);
		expect(before.defaultPrevented).toBe(true);
		await expect(api('POST', '/api/v1/test', {})).rejects.toThrow();
		const after = new Event('beforeunload', { cancelable: true });
		window.dispatchEvent(after);
		expect(after.defaultPrevented).toBe(false);
		expect(session.me).toBeNull();
	} finally {
		Object.defineProperty(window, 'location', { value: realLocation, writable: true });
		registerReloginPrompt(null);
		vi.unstubAllGlobals();
		session.clear();
	}
});

it('asks once for nested editors and removes guards on teardown', async () => {
	session.setMe({ user: { id: 1, name: 'Owner', email: 'owner@example.com' }, orgs: [] });
	const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
	const start = beforeNavigateMock.mock.calls.length;
	const first = render(Dialog, { title: 'First', open: true, children });
	const second = render(Dialog, { title: 'Second', open: true, children });
	await tick();
	await fireEvent.input(screen.getAllByRole('textbox')[1], { target: { value: 'Changed' } });
	const cancel = vi.fn();
	for (const [navigate] of beforeNavigateMock.mock.calls.slice(start)) navigate({ to: { url: new URL('http://localhost/acme') }, cancel });
	expect(confirm).toHaveBeenCalledOnce();
	expect(cancel).toHaveBeenCalledOnce();
	first.unmount();
	const pending = new Event('beforeunload', { cancelable: true });
	window.dispatchEvent(pending);
	expect(pending.defaultPrevented).toBe(true);
	second.unmount();
	const clean = new Event('beforeunload', { cancelable: true });
	window.dispatchEvent(clean);
	expect(clean.defaultPrevented).toBe(false);
	vi.restoreAllMocks(); session.clear();
});
