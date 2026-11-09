import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ReloginDialog from './ReloginDialog.svelte';
import { session } from '../session.svelte';

// jsdom doesn't implement <dialog>'s showModal/close - Dialog.svelte calls
// them directly, so stub them out rather than exercising the real modal
// behaviour (that's native browser behaviour, not this component's logic).
beforeEach(() => {
	HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
		this.setAttribute('open', '');
	});
	HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
		this.removeAttribute('open');
	});
});

function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

describe('ReloginDialog', () => {
	it('tears down the expired session before leaving for SSO', async () => {
		session.setMe({ user: { id: 1, name: 'Owner', email: 'owner@example.com' }, orgs: [] });
		render(ReloginDialog, { open: true, email: 'owner@example.com', sso: true, onSuccess: vi.fn(), onCancel: vi.fn() });
		const link = screen.getByRole('link', { name: 'Sign in with SSO' });
		link.addEventListener('click', (event) => event.preventDefault());
		await fireEvent.click(link);
		expect(session.me).toBeNull();
	});
	it('blocks Escape while sign-in is pending', async () => {
		vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
		render(ReloginDialog, { open: true, email: 'staffer@example.com', onSuccess: vi.fn(), onCancel: vi.fn() });
		await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
		const event = new Event('cancel', { cancelable: true });
		screen.getByRole('dialog').dispatchEvent(event);
		expect(event.defaultPrevented).toBe(true);
		expect(screen.getByRole('dialog').getAttribute('aria-busy')).toBe('true');
		vi.unstubAllGlobals();
	});
	it('pre-fills the email and posts it with the entered password', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, {})));
		const onSuccess = vi.fn();

		render(ReloginDialog, {
			props: { open: true, email: 'staffer@example.com', onSuccess, onCancel: vi.fn() }
		});

		expect((screen.getByLabelText('Email') as HTMLInputElement).value).toBe(
			'staffer@example.com'
		);

		await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));

		expect(fetch).toHaveBeenCalledWith(
			'/api/v1/auth/login',
			expect.objectContaining({
				method: 'POST',
				body: JSON.stringify({ email: 'staffer@example.com', password: 'secret' })
			})
		);
		expect(onSuccess).toHaveBeenCalledOnce();

		vi.unstubAllGlobals();
	});

	it("shows the server's error inside the dialog on a failed attempt", async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn().mockResolvedValue(jsonResponse(401, { error: 'invalid credentials' }))
		);

		render(ReloginDialog, {
			props: { open: true, email: 'staffer@example.com', onSuccess: vi.fn(), onCancel: vi.fn() }
		});

		await fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'wrong' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));

		expect(await screen.findByText('invalid credentials')).toBeTruthy();

		vi.unstubAllGlobals();
	});

	it('calls onCancel when Cancel is clicked', async () => {
		vi.stubGlobal('fetch', vi.fn());
		const onCancel = vi.fn();

		render(ReloginDialog, {
			props: { open: true, email: 'staffer@example.com', onSuccess: vi.fn(), onCancel }
		});

		await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

		expect(onCancel).toHaveBeenCalledOnce();
		expect(fetch).not.toHaveBeenCalled();

		vi.unstubAllGlobals();
	});

	it('shows a full-page SSO redirect link when sso is true', () => {
		const assign = vi.fn();
		Object.defineProperty(window, 'location', {
			value: { pathname: '/acme/invoices', search: '?foo=bar', assign },
			writable: true
		});

		render(ReloginDialog, {
			props: { open: true, email: 'staffer@example.com', sso: true, onSuccess: vi.fn(), onCancel: vi.fn() }
		});

		const link = screen.getByRole('link', { name: /sign in with sso/i }) as HTMLAnchorElement;
		expect(link.getAttribute('href')).toBe(
			'/api/v1/auth/oidc/start?next=' + encodeURIComponent('/acme/invoices?foo=bar')
		);
	});

	it('omits the SSO option when sso is false', () => {
		render(ReloginDialog, {
			props: { open: true, email: 'staffer@example.com', sso: false, onSuccess: vi.fn(), onCancel: vi.fn() }
		});

		expect(screen.queryByRole('link', { name: /sign in with sso/i })).toBeNull();
	});

	it('calls onCancel when the native dialog is dismissed (Escape/backdrop)', () => {
		vi.stubGlobal('fetch', vi.fn());
		const onCancel = vi.fn();

		const { container } = render(ReloginDialog, {
			props: { open: true, email: 'staffer@example.com', onSuccess: vi.fn(), onCancel }
		});

		// jsdom doesn't implement real Escape-to-close or backdrop-click
		// dismissal; both fire the same native "close" event Dialog.svelte
		// listens for, so dispatching it directly exercises the same path.
		const dialogEl = container.querySelector('dialog');
		dialogEl?.dispatchEvent(new Event('close'));

		expect(onCancel).toHaveBeenCalledOnce();
		expect(fetch).not.toHaveBeenCalled();

		vi.unstubAllGlobals();
	});
});
