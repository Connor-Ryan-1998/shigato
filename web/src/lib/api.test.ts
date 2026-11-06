import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, resetOnUnauthorized, setOnUnauthorized } from './api';

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
		setOnUnauthorized(() => {});
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

	it('calls onUnauthorized on a 401 response', async () => {
		vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));
		const onUnauthorized = vi.fn();
		setOnUnauthorized(onUnauthorized);

		await expect(api('GET', '/api/v1/thing')).rejects.toThrow(ApiError);

		expect(onUnauthorized).toHaveBeenCalledOnce();
	});

	it('returns undefined for a 2xx response with an empty body', async () => {
		vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));

		await expect(api('DELETE', '/api/v1/thing')).resolves.toBeUndefined();
	});

	describe('the default onUnauthorized', () => {
		const realLocation = window.location;

		afterEach(() => {
			setOnUnauthorized(() => {});
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

		it('navigates to /login with next when elsewhere', async () => {
			resetOnUnauthorized();
			const assign = stubLocation('/acme/invoices');
			vi.mocked(fetch).mockResolvedValue(jsonResponse(401, { error: 'unauthorized' }));

			await expect(api('GET', '/api/v1/thing')).rejects.toThrow(ApiError);

			expect(assign).toHaveBeenCalledWith('/login?next=%2Facme%2Finvoices');
		});
	});
});
