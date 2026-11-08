import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, registerReloginPrompt, resetOnUnauthorized, setOnUnauthorized } from './api';

function jsonResponse(status: number, body: unknown, statusText = ''): Response {
	return new Response(JSON.stringify(body), {
		status,
		statusText,
		headers: { 'Content-Type': 'application/json' }
	});
}

describe('api', () => {
	beforeEach(() => {
		vi.stubGlobal('fetch', vi.fn());
		setOnUnauthorized(async () => false);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it('sends the CSRF header on POST', async () => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(200, { ok: true }));

		await api('POST', '/api/v1/thing', { a: 1 });

		const [, init] = vi.mocked(fetch).mock.calls[0];
		const headers = init?.headers as Record<string, string>;
		expect(headers['X-Shigoto-CSRF']).toBe('1');
	});

	it('does not send the CSRF header on GET', async () => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(200, { ok: true }));

		await api('GET', '/api/v1/thing');

		const [, init] = vi.mocked(fetch).mock.calls[0];
		const headers = (init?.headers ?? {}) as Record<string, string>;
		expect(headers['X-Shigoto-CSRF']).toBeUndefined();
	});

	it('throws ApiError with the body error message on a non-2xx response', async () => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(422, { error: 'bad' }));

		await expect(api('POST', '/api/v1/thing', {})).rejects.toMatchObject(
			new ApiError(422, 'bad')
		);
	});
	it('preserves retained invoice ID on billing-run error', async () => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(422, { error: 'issue failed', invoice_id: 77 }));
		await expect(api('POST', '/api/v1/orgs/acme/billing-runs', {})).rejects.toMatchObject({
			status: 422, message: 'issue failed', invoiceID: 77
		});
	});
	it.each([undefined, null, '77', -1, 0, 1.5, Number.MAX_SAFE_INTEGER + 1])('does not expose invalid invoice ID %s', async (invoice_id) => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(422, { error: 'bad', invoice_id }));
		await expect(api('POST', '/api/v1/orgs/acme/billing-runs', {})).rejects.toMatchObject({ status: 422, message: 'bad', invoiceID: undefined });
	});

	it('calls onUnauthorized on a 401 response and throws when it resolves false', async () => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));
		const onUnauthorized = vi.fn(async () => false);
		setOnUnauthorized(onUnauthorized);

		await expect(api('GET', '/api/v1/thing')).rejects.toThrow(ApiError);

		expect(onUnauthorized).toHaveBeenCalledOnce();
	});

	it('retries the request once when onUnauthorized resolves true', async () => {
		vi.mocked(fetch)
			.mockResolvedValueOnce(jsonResponse(401, { error: 'unauthorized' }))
			.mockResolvedValueOnce(jsonResponse(200, { ok: true }));
		setOnUnauthorized(async () => true);

		await expect(api('GET', '/api/v1/thing')).resolves.toEqual({ ok: true });

		expect(fetch).toHaveBeenCalledTimes(2);
	});

	it('does not retry forever if the retried request 401s again, and redirects instead of failing silently', async () => {
		const realLocation = window.location;
		const assign = vi.fn();
		Object.defineProperty(window, 'location', {
			value: { pathname: '/acme/invoices', search: '', assign },
			writable: true
		});

		vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));
		const onUnauthorized = vi.fn(async () => true);
		setOnUnauthorized(onUnauthorized);

		await expect(api('GET', '/api/v1/thing')).rejects.toThrow(ApiError);

		// Once for the original request, once for the single retry - not
		// called again for the retry's own 401.
		expect(fetch).toHaveBeenCalledTimes(2);
		expect(onUnauthorized).toHaveBeenCalledOnce();
		// The session was invalidated again immediately (or never really
		// took): redirect rather than leave the caller with what looks like
		// a silently-swallowed rejection.
		expect(assign).toHaveBeenCalledWith('/login?next=%2Facme%2Finvoices');

		Object.defineProperty(window, 'location', { value: realLocation, writable: true });
	});

	it('returns undefined for a 2xx response with an empty body', async () => {
		vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

		await expect(api('DELETE', '/api/v1/thing')).resolves.toBeUndefined();
	});

	describe('the default onUnauthorized', () => {
		const realLocation = window.location;

		afterEach(() => {
			setOnUnauthorized(async () => false);
			registerReloginPrompt(null);
			Object.defineProperty(window, 'location', { value: realLocation, writable: true });
		});

		function stubLocation(pathname: string) {
			const assign = vi.fn();
			Object.defineProperty(window, 'location', {
				value: { pathname, search: '', assign },
				writable: true
			});
			return assign;
		}

		it('does nothing when already on /login (lets the caller show the error instead)', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/login');
			vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'invalid credentials' }));

			await expect(api('POST', '/api/v1/auth/login', {})).rejects.toMatchObject(
				new ApiError(401, 'invalid credentials')
			);

			expect(assign).not.toHaveBeenCalled();
		});

		it('navigates to /login with next when no relogin prompt is registered', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/acme/invoices');
			vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));

			await expect(api('GET', '/api/v1/thing')).rejects.toThrow(ApiError);

			expect(assign).toHaveBeenCalledWith('/login?next=%2Facme%2Finvoices');
		});

		it('opens the registered relogin prompt and retries on success', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/acme/invoices');
			vi.mocked(fetch)
				.mockResolvedValueOnce(jsonResponse(401, { error: 'unauthorized' }))
				.mockResolvedValueOnce(jsonResponse(200, { ok: true }));
			registerReloginPrompt(async () => true);

			await expect(api('GET', '/api/v1/thing')).resolves.toEqual({ ok: true });

			expect(assign).not.toHaveBeenCalled();
		});

		it('redirects to /login under /portal even with a relogin prompt registered (Ruling 50)', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/portal/acme/invoices');
			vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));
			const prompt = vi.fn(async () => true);
			registerReloginPrompt(prompt);

			await expect(api('GET', '/api/v1/portal/acme/invoices')).rejects.toThrow(ApiError);

			expect(prompt).not.toHaveBeenCalled();
			expect(assign).toHaveBeenCalledWith('/login?next=%2Fportal%2Facme%2Finvoices');
		});

		it('does not redirect on a 401 under /invite/<token> (an anonymous visitor accepting an invite)', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/invite/some-token');
			vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));
			const prompt = vi.fn(async () => true);
			registerReloginPrompt(prompt);

			await expect(api('GET', '/api/v1/me')).rejects.toThrow(ApiError);

			expect(prompt).not.toHaveBeenCalled();
			expect(assign).not.toHaveBeenCalled();
		});

		it('falls back to the redirect when the relogin prompt is cancelled', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/acme/invoices');
			vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));
			registerReloginPrompt(async () => false);

			await expect(api('GET', '/api/v1/thing')).rejects.toThrow(ApiError);

			expect(assign).toHaveBeenCalledWith('/login?next=%2Facme%2Finvoices');
		});

		it('shares one relogin prompt across two concurrent 401s, then retries both', async () => {
			resetOnUnauthorized();
			stubLocation('/acme/invoices');
			// Both calls' first attempt 401s; both retries (after the shared
			// prompt resolves) succeed.
			vi.mocked(fetch)
				.mockResolvedValueOnce(jsonResponse(401, { error: 'unauthorized' }))
				.mockResolvedValueOnce(jsonResponse(401, { error: 'unauthorized' }))
				.mockResolvedValueOnce(jsonResponse(200, { ok: true }))
				.mockResolvedValueOnce(jsonResponse(200, { ok: true }));

			let resolvePrompt: (ok: boolean) => void = () => {};
			const prompt = vi.fn(
				() => new Promise<boolean>((resolve) => (resolvePrompt = resolve))
			);
			registerReloginPrompt(prompt);

			const first = api('GET', '/api/v1/a');
			const second = api('GET', '/api/v1/b');

			// Both api() calls' fetch + 401 handling run (microtasks) before
			// either sees a resolved prompt.
			await vi.waitFor(() => expect(prompt).toHaveBeenCalledOnce());
			resolvePrompt(true);

			await expect(first).resolves.toEqual({ ok: true });
			await expect(second).resolves.toEqual({ ok: true });
			expect(prompt).toHaveBeenCalledOnce();
		});
	});
});
