import { describe, expect, it } from 'vitest';
import { page as appPage } from '$app/state';
import { goto } from '$app/navigation';
import { api, ApiError } from '../api';
import { apiError, apiMock, assertAllRouted, deferred, mockApi, pending } from './mock-api';
import { setPage } from './app.svelte';
import { signIn } from './session';
import { session } from '../session.svelte';

describe('shared api mock', () => {
	it('resolves routed data', async () => {
		mockApi({ 'GET /api/v1/orgs/acme/contacts': [{ id: 1 }] });
		await expect(api('GET', '/api/v1/orgs/acme/contacts')).resolves.toEqual([{ id: 1 }]);
	});

	it('passes the request body to function routes', async () => {
		mockApi({ 'POST /api/v1/orgs/acme/contacts': (body) => ({ id: 2, ...(body as object) }) });
		await expect(api('POST', '/api/v1/orgs/acme/contacts', { name: 'X' })).resolves.toEqual({ id: 2, name: 'X' });
	});

	it('rejects with the real ApiError status and message', async () => {
		mockApi({ 'PUT /api/v1/orgs/acme/settings': apiError(422, 'bad bsb') });
		const err = (await api('PUT', '/api/v1/orgs/acme/settings', {}).catch((e) => e)) as ApiError;
		expect(err).toBeInstanceOf(ApiError);
		expect(err.status).toBe(422);
		expect(err.message).toBe('bad bsb');
	});

	it('throws on an unrouted call naming method and path', () => {
		mockApi({});
		expect(() => api('DELETE', '/api/v1/orgs/acme/contacts/9')).toThrow(
			'unrouted api call: DELETE /api/v1/orgs/acme/contacts/9'
		);
		expect(() => assertAllRouted()).toThrow('DELETE /api/v1/orgs/acme/contacts/9');
	});

	it('fails the test after a swallowed unrouted call', async () => {
		mockApi({});
		// A component's catch turns the throw into page text; the test must
		// still fail.
		await Promise.resolve().then(() => api('GET', '/swallowed')).catch(() => 'Could not load.');
		expect(() => assertAllRouted()).toThrow('unrouted api call: GET /swallowed');
		// assertAllRouted cleared it, so this test's own afterEach passes.
		expect(() => assertAllRouted()).not.toThrow();
	});

	it('records calls on apiMock', async () => {
		mockApi({ 'GET /x': 1 });
		await api('GET', '/x');
		expect(apiMock).toHaveBeenCalledWith('GET', '/x');
	});

	it('pending never settles and deferred settles on demand', async () => {
		let settled = false;
		void pending().then(() => (settled = true));
		const d = deferred<number>();
		mockApi({ 'GET /d': d.promise });
		const got = api('GET', '/d');
		d.resolve(5);
		await expect(got).resolves.toBe(5);
		expect(settled).toBe(false);
	});

	it('sets a route in one test', () => {
		mockApi({ 'GET /leak': 1 });
	});

	it('starts the next test with no routes and no recorded calls', () => {
		expect(apiMock).not.toHaveBeenCalled();
		expect(() => api('GET', '/leak')).toThrow('unrouted api call: GET /leak');
		expect(() => assertAllRouted()).toThrow('GET /leak');
	});
});

describe('shared app mocks', () => {
	it('setPage updates params read through $app/state', () => {
		setPage({ params: { org: 'beta' } });
		expect(appPage.params.org).toBe('beta');
	});

	it('resets page params and goto between tests', () => {
		expect(appPage.params).toEqual({});
		expect(goto).not.toHaveBeenCalled();
	});

	it('defaults window.confirm to true', () => {
		expect(window.confirm('discard?')).toBe(true);
	});
});

describe('shared dialog polyfill', () => {
	it('opens and closes a dialog', () => {
		const dialog = document.createElement('dialog');
		dialog.showModal();
		expect(dialog.open).toBe(true);
		dialog.close();
		expect(dialog.open).toBe(false);
	});
});

describe('session reset', () => {
	it('leaves a signed-in session behind', () => {
		signIn({ role: 'owner' });
		session.setModules(['property']);
	});

	it('starts the next test signed out with no modules', () => {
		expect(session.me).toBeNull();
		expect(session.orgSlug).toBeNull();
		expect(session.modules).toEqual([]);
	});
});

describe('signIn', () => {
	it('signs in to acme with the given role and perms', () => {
		signIn({ role: 'bookkeeper', perms: ['invoices_read'] });
		expect(session.orgSlug).toBe('acme');
		expect(session.currentOrg).toMatchObject({ slug: 'acme', role: 'bookkeeper', perms: ['invoices_read'] });
	});
});
