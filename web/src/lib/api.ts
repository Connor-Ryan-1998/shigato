// api is the one place that talks to the Go backend. It adds the CSRF
// header unsafe requests need (internal/httpapi/csrf.go), unwraps the
// {"error": "..."} body every failed request gets, and sends 401s through a
// single injectable hook instead of leaving every call site to notice it.

export class ApiError extends Error {
	status: number;

	constructor(status: number, message: string) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
	}
}

const unsafeMethods = new Set(['POST', 'PUT', 'DELETE']);

// OnUnauthorized handles a 401 from any api() call. It resolves true when
// the caller should retry the original request (the user re-authenticated
// in place) or false when it should give up (the default then falls back
// to redirecting to /login).
export type OnUnauthorized = () => Promise<boolean>;

// ReloginPrompt asks the user to sign back in without leaving the page
// they're on, and resolves with whether they did. The root layout
// registers the mounted ReloginDialog as this via registerReloginPrompt.
export type ReloginPrompt = () => Promise<boolean>;

let reloginPrompt: ReloginPrompt | null = null;

// registerReloginPrompt wires defaultOnUnauthorized up to the mounted
// ReloginDialog. Pass null (component teardown, or no dialog mounted - e.g.
// in tests) to fall back to the plain redirect.
export function registerReloginPrompt(fn: ReloginPrompt | null): void {
	reloginPrompt = fn;
}

function redirectToLogin(): void {
	if (typeof window === 'undefined') return;
	const next = window.location.pathname + window.location.search;
	window.location.assign(`/login?next=${encodeURIComponent(next)}`);
}

// pendingRelogin is shared across concurrent 401s so they open (and await)
// one dialog instead of one each.
let pendingRelogin: Promise<boolean> | null = null;

async function defaultOnUnauthorized(): Promise<boolean> {
	if (typeof window === 'undefined') return false;
	// Already on /login: a 401 here is the login form's own failed attempt
	// (or a stale /me call racing it), not a session that needs to bounce
	// anywhere. Reloading /login would also wipe out the "invalid
	// credentials" state the login page is about to render from this same
	// rejection.
	if (window.location.pathname === '/login') return false;

	if (!reloginPrompt) {
		redirectToLogin();
		return false;
	}

	if (!pendingRelogin) {
		const prompt = reloginPrompt;
		pendingRelogin = prompt().finally(() => {
			pendingRelogin = null;
		});
	}
	const reloggedIn = await pendingRelogin;
	if (!reloggedIn) {
		redirectToLogin();
	}
	return reloggedIn;
}

let onUnauthorized: OnUnauthorized = defaultOnUnauthorized;

// setOnUnauthorized swaps the 401 handler. Production code never needs to
// call this (the default opens ReloginDialog, or redirects to /login when
// none is registered); tests use it to observe a 401 without a real dialog
// or navigation.
export function setOnUnauthorized(fn: OnUnauthorized): void {
	onUnauthorized = fn;
}

// resetOnUnauthorized restores the default (ReloginDialog/redirect) 401
// handler. Only api.test.ts calls this, to test the default itself rather
// than a test double.
export function resetOnUnauthorized(): void {
	onUnauthorized = defaultOnUnauthorized;
}

export async function api<T>(
	method: 'GET' | 'POST' | 'PUT' | 'DELETE',
	path: string,
	body?: unknown | FormData,
	// Internal: set on api()'s own recursive call after a successful
	// re-login, so a request that 401s again right after can't retry
	// forever. Callers never pass this.
	isRetry = false
): Promise<T> {
	const headers: Record<string, string> = {};
	let requestBody: BodyInit | undefined;

	if (body instanceof FormData) {
		requestBody = body;
	} else if (body !== undefined) {
		headers['Content-Type'] = 'application/json';
		requestBody = JSON.stringify(body);
	}

	if (unsafeMethods.has(method)) {
		headers['X-Shigoto-CSRF'] = '1';
	}

	const res = await fetch(path, {
		method,
		headers,
		body: requestBody,
		credentials: 'same-origin'
	});

	if (!res.ok) {
		let message = res.statusText;
		try {
			const data = await res.clone().json();
			if (data && typeof data.error === 'string') {
				message = data.error;
			}
		} catch {
			// Body wasn't JSON (or was empty): fall back to statusText.
		}
		if (res.status === 401) {
			if (!isRetry) {
				const reloggedIn = await onUnauthorized();
				if (reloggedIn) {
					return api<T>(method, path, body, true);
				}
			} else {
				// The retried request 401'd again right after a successful
				// re-login - the session was invalidated again immediately,
				// or never really took. Redirect rather than leave the
				// caller with what would otherwise look like a silent,
				// uncaught rejection.
				redirectToLogin();
			}
		}
		throw new ApiError(res.status, message);
	}

	// An empty body (204, or a 2xx with no content) is never valid JSON, so
	// decoding it unconditionally would throw even though the request
	// succeeded; read it as text first and only parse when there's
	// something to parse.
	const text = await res.text();
	if (text === '') {
		return undefined as T;
	}
	return JSON.parse(text) as T;
}
