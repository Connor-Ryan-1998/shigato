// mock-api drives the global lib/api mock installed by setup.ts. Every test
// starts with no routes: an api() call nobody routed throws, so a test that
// needs the real client (and forgot vi.unmock) fails loudly instead of
// reading undefined.

import { ApiError } from '../api';
import { apiMock } from './api-spy';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Route = ((body: any) => unknown) | object | string | number | boolean | null | undefined;

const rejection = Symbol('rejection');
type Rejection = { [rejection]: ApiError };

// apiMock is the function the mocked api() delegates to. Tests may still
// drive it directly (mockImplementation, mock.calls) where a route table
// doesn't fit.
export { apiMock };

// mockApi routes 'METHOD /full/path' keys to a value: data resolves,
// apiError(...) rejects, a promise is returned as-is (pending/deferred), and
// a function receives the request body and returns any of those.
export function mockApi(routes: Record<string, Route>): void {
	apiMock.mockImplementation((method, path, body) => {
		const key = `${method} ${path}`;
		if (!(key in routes)) {
			unrouted.push(key);
			throw new Error(`unrouted api call: ${key}`);
		}
		let value: unknown = routes[key];
		if (typeof value === 'function') value = (value as (b: unknown) => unknown)(body);
		if (value && typeof value === 'object' && rejection in value) {
			return Promise.reject((value as Rejection)[rejection]);
		}
		return value instanceof Promise ? value : Promise.resolve(value);
	});
}

// apiError is a route value that rejects with the real ApiError.
export function apiError(status: number, message: string, invoiceID?: number): Rejection {
	return { [rejection]: new ApiError(status, message, invoiceID) };
}

// pending is a route value that never settles: the save stays in flight.
export function pending<T = never>(): Promise<T> {
	return new Promise<T>(() => {});
}

// deferred is a route value the test settles itself, to order responses.
export function deferred<T = unknown>() {
	let resolve!: (value: T) => void;
	let reject!: (reason: unknown) => void;
	const promise = new Promise<T>((ok, fail) => {
		resolve = ok;
		reject = fail;
	});
	return { promise, resolve, reject };
}

// jsonResponse builds a fetch Response for tests that stub fetch and use
// the real api client.
export function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

// unrouted records calls no route answered. Components often catch the
// throw and show generic text, so setup.ts fails the test afterwards too.
const unrouted: string[] = [];

// assertAllRouted throws if an unrouted call happened since the last check,
// and clears the record.
export function assertAllRouted(): void {
	const calls = unrouted.splice(0);
	if (calls.length) throw new Error(`unrouted api call: ${calls.join(', ')} (add a mockApi route)`);
}

export function resetApiMock(): void {
	unrouted.length = 0;
	apiMock.mockReset();
	mockApi({});
}
