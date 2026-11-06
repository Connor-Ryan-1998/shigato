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

function defaultOnUnauthorized(): void {
	if (typeof window === 'undefined') return;
	const next = window.location.pathname + window.location.search;
	window.location.assign(`/login?next=${encodeURIComponent(next)}`);
}

let onUnauthorized: () => void = defaultOnUnauthorized;

// setOnUnauthorized swaps the 401 handler. Production code never needs to
// call this (the default navigates to /login); tests use it to observe a
// 401 without a real navigation.
export function setOnUnauthorized(fn: () => void): void {
	onUnauthorized = fn;
}

export async function api<T>(
	method: 'GET' | 'POST' | 'PUT' | 'DELETE',
	path: string,
	body?: unknown | FormData
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
			onUnauthorized();
		}
		throw new ApiError(res.status, message);
	}

	if (res.status === 204) {
		return undefined as T;
	}
	return (await res.json()) as T;
}
