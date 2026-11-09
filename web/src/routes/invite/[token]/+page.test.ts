import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';

const page = { params: { token: 'test-token' } };
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));
const { goto } = vi.hoisted(() => ({ goto: vi.fn() }));
vi.mock('$app/navigation', () => ({ goto }));

import InvitePage from './+page.svelte';
import { session, type MeResponse } from '../../../lib/session.svelte';

function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

const baseDetails = {
	org_name: 'Acme',
	email: 'invitee@example.com',
	role: 'staff',
	expired: false,
	needs_password: false
};

describe('invite accept page', () => {
	it('blocks repeat acceptance and locks the draft during submission', async () => {
		vi.stubGlobal('fetch', vi.fn((path: RequestInfo | URL) => String(path).endsWith('/accept') ? new Promise<Response>(() => {}) : Promise.resolve(jsonResponse(200, baseDetails))));
		render(InvitePage);
		await screen.findByText('Join Acme');
		const form = screen.getByRole('button', { name: 'Create account and accept' }).closest('form')!;
		await fireEvent.submit(form);
		await fireEvent.submit(form);
		expect(vi.mocked(fetch).mock.calls.filter(([path]) => String(path).endsWith('/accept'))).toHaveLength(1);
		expect(screen.getByLabelText('Password').matches(':disabled')).toBe(true);
	});
	beforeEach(() => {
		session.clear();
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		goto.mockClear();
	});

	// Item 1 (critical): the anonymous accept form - name + password - must
	// render and submit for a brand-new visitor with no session at all.
	it('renders the anonymous name/password form and submits it', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn((path: RequestInfo | URL) => {
				const url = path.toString();
				if (url.includes('/invites/test-token/accept')) {
					return Promise.resolve(jsonResponse(200, { org_slug: 'acme' }));
				}
				if (url.includes('/invites/test-token')) {
					return Promise.resolve(jsonResponse(200, baseDetails));
				}
				if (url.includes('/me')) {
					const me: MeResponse = {
						user: { id: 1, email: 'invitee@example.com', name: 'Invitee' },
						orgs: [{ slug: 'acme', name: 'Acme', role: 'staff', perms: [] }]
					};
					return Promise.resolve(jsonResponse(200, me));
				}
				if (url.includes('/meta')) {
					return Promise.resolve(jsonResponse(200, { modules: [] }));
				}
				return Promise.resolve(jsonResponse(404, { error: 'not found' }));
			})
		);

		render(InvitePage);

		await screen.findByText('Join Acme');
		expect(screen.getByLabelText('Your name')).toBeTruthy();
		const passwordInput = screen.getByLabelText('Password') as HTMLInputElement;

		await fireEvent.input(screen.getByLabelText('Your name'), { target: { value: 'New Staff' } });
		await fireEvent.input(passwordInput, { target: { value: 'a-very-long-password' } });
		await fireEvent.click(screen.getByRole('button', { name: /create account and accept/i }));

		await waitFor(() => expect(goto).toHaveBeenCalledWith('/acme'));
	});

	// Item 2 (Ruling 63): a signed-in user with a password already (or OIDC
	// enabled) sees no password field and accepts with one click.
	it('accepts with a single click when already signed in and no password is required', async () => {
		session.setMe({
			user: { id: 2, email: 'invitee@example.com', name: 'Invitee' },
			orgs: []
		});

		vi.stubGlobal(
			'fetch',
			vi.fn((path: RequestInfo | URL) => {
				const url = path.toString();
				if (url.includes('/invites/test-token/accept')) {
					return Promise.resolve(jsonResponse(200, { org_slug: 'acme' }));
				}
				if (url.includes('/invites/test-token')) {
					return Promise.resolve(jsonResponse(200, baseDetails));
				}
				if (url.includes('/me')) {
					const me: MeResponse = {
						user: { id: 2, email: 'invitee@example.com', name: 'Invitee' },
						orgs: [{ slug: 'acme', name: 'Acme', role: 'staff', perms: [] }]
					};
					return Promise.resolve(jsonResponse(200, me));
				}
				if (url.includes('/meta')) {
					return Promise.resolve(jsonResponse(200, { modules: [] }));
				}
				return Promise.resolve(jsonResponse(404, { error: 'not found' }));
			})
		);

		render(InvitePage);

		await screen.findByText('Join Acme');
		expect(screen.queryByLabelText('Your name')).toBeNull();
		expect(screen.queryByLabelText(/password/i)).toBeNull();

		await fireEvent.click(screen.getByRole('button', { name: /accept invite/i }));

		await waitFor(() => expect(goto).toHaveBeenCalledWith('/acme'));
	});

	// Item 2 (Ruling 63): a signed-in user with no password (a portal-only
	// account) must see and submit a password field, even though they're
	// already signed in.
	it('shows a password field for a signed-in user who needs one (Ruling 63)', async () => {
		session.setMe({
			user: { id: 3, email: 'invitee@example.com', name: 'Invitee' },
			orgs: []
		});

		let acceptedBody: unknown;
		vi.stubGlobal(
			'fetch',
			vi.fn((path: RequestInfo | URL, init?: RequestInit) => {
				const url = path.toString();
				if (url.includes('/invites/test-token/accept')) {
					acceptedBody = init?.body ? JSON.parse(init.body as string) : undefined;
					return Promise.resolve(jsonResponse(200, { org_slug: 'acme' }));
				}
				if (url.includes('/invites/test-token')) {
					return Promise.resolve(jsonResponse(200, { ...baseDetails, needs_password: true }));
				}
				if (url.includes('/me')) {
					const me: MeResponse = {
						user: { id: 3, email: 'invitee@example.com', name: 'Invitee' },
						orgs: [{ slug: 'acme', name: 'Acme', role: 'staff', perms: [] }]
					};
					return Promise.resolve(jsonResponse(200, me));
				}
				if (url.includes('/meta')) {
					return Promise.resolve(jsonResponse(200, { modules: [] }));
				}
				return Promise.resolve(jsonResponse(404, { error: 'not found' }));
			})
		);

		render(InvitePage);

		await screen.findByText('Join Acme');
		const passwordInput = screen.getByLabelText('Set a password') as HTMLInputElement;
		await fireEvent.input(passwordInput, { target: { value: 'a-very-long-password' } });
		await fireEvent.click(screen.getByRole('button', { name: /accept invite/i }));

		await waitFor(() => expect(goto).toHaveBeenCalledWith('/acme'));
		expect(acceptedBody).toEqual({ password: 'a-very-long-password' });
	});

	it.each([
		[409, 'this invite is for a different email address'],
		[410, 'this invite has already been used or has expired'],
		[422, 'password must be at least 12 characters']
	])('shows the server error message for a %i accept response', async (status, message) => {
		vi.stubGlobal(
			'fetch',
			vi.fn((path: RequestInfo | URL) => {
				const url = path.toString();
				if (url.includes('/invites/test-token/accept')) {
					return Promise.resolve(jsonResponse(status, { error: message }));
				}
				if (url.includes('/invites/test-token')) {
					return Promise.resolve(jsonResponse(200, baseDetails));
				}
				if (url.includes('/me')) {
					return Promise.resolve(jsonResponse(401, { error: 'unauthorized' }));
				}
				if (url.includes('/meta')) {
					return Promise.resolve(jsonResponse(200, { modules: [] }));
				}
				return Promise.resolve(jsonResponse(404, { error: 'not found' }));
			})
		);

		render(InvitePage);

		await screen.findByText('Join Acme');
		await fireEvent.input(screen.getByLabelText('Your name'), { target: { value: 'New Staff' } });
		await fireEvent.input(screen.getByLabelText('Password'), {
			target: { value: 'a-very-long-password' }
		});
		await fireEvent.click(screen.getByRole('button', { name: /create account and accept/i }));

		await waitFor(() => expect(screen.getByRole('alert').textContent).toBe(message));
		expect(goto).not.toHaveBeenCalled();
	});
});
