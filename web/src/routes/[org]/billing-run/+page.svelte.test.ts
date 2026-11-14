import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { jsonResponse } from '../../../lib/test/mock-api';
import { goto, page, setPage } from '../../../lib/test/app.svelte';
// Exercises the real api client against a stubbed fetch.
vi.unmock('../../../lib/api');
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

import Page from './+page.svelte';

beforeEach(() => {
	setPage({ params: { org: 'acme' } });
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'owner', perms: ['invoices_read', 'invoices_write'] })) });
	session.setOrg('acme');
	goto.mockReset();
	vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => Promise.resolve(init?.method === 'GET' ?
		jsonResponse(200, [{ id: 2, name: 'Client Co', kind: 'client' }]) : jsonResponse(422, { error: 'Issuing failed', invoice_id: 77 }))));
});
afterEach(() => vi.unstubAllGlobals());
async function form() {
	await screen.findByText('Client Co');
	return screen.getByRole('button', { name: 'Create invoice' }).closest('form')!;
}
describe('billing-run recovery', () => {
	it('does not create another invoice if navigation fails after a successful run', async () => {
		vi.mocked(fetch).mockImplementation((_url, init) => Promise.resolve(init?.method === 'GET' ? jsonResponse(200, [{ id: 2, name: 'Client Co', kind: 'client' }]) : jsonResponse(200, { invoice: { id: 77 }, entry_count: 1, total_minutes: 60 })));
		goto.mockRejectedValue(new Error('Navigation failed'));
		render(Page);
		const runForm = await form();
		await fireEvent.submit(runForm);
		await screen.findByRole('link', { name: 'Open existing invoice' });
		await fireEvent.submit(runForm);
		expect(vi.mocked(fetch).mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
	});
	it('links retained invoice and warns entries are already billed', async () => {
		render(Page);
		await fireEvent.submit(await form());
		const link = await screen.findByRole('link', { name: 'Open existing invoice' });
		expect(link.getAttribute('href')).toBe('/acme/invoices/77');
		expect(screen.getByText(/Time entries are already billed/)).toBeTruthy();
		expect(goto).not.toHaveBeenCalled();
	});
	it('does not invent recovery link without valid invoice ID', async () => {
		vi.mocked(fetch).mockImplementation((_url, init) => Promise.resolve(init?.method === 'GET' ? jsonResponse(200, [{ id: 2, name: 'Client Co', kind: 'client' }]) : jsonResponse(422, { error: 'No unbilled entries' })));
		render(Page);
		await fireEvent.submit(await form());
		await screen.findByText('No unbilled entries');
		expect(screen.queryByRole('link', { name: 'Open existing invoice' })).toBeNull();
	});
	it('guards duplicate run and ignores old org recovery response', async () => {
		let finish!: (v: Response) => void;
		vi.mocked(fetch).mockImplementation((_url, init) => init?.method === 'GET' ? Promise.resolve(jsonResponse(200, [{ id: 2, name: 'Client Co', kind: 'client' }])) : new Promise((resolve) => { finish = resolve; }));
		render(Page);
		const runForm = await form();
		await fireEvent.submit(runForm);
		await fireEvent.submit(runForm);
		expect(vi.mocked(fetch).mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await screen.findByText('Client Co');
		finish(jsonResponse(422, { error: 'Old error', invoice_id: 77 }));
		await Promise.resolve();
		await Promise.resolve();
		flushSync();
		expect(screen.queryByRole('link', { name: 'Open existing invoice' })).toBeNull();
		expect(screen.queryByText('Old error')).toBeNull();
	});
	it('cannot submit old org form before effects run', async () => {
		render(Page);
		const runForm = await form();
		page.params.org = 'other';
		runForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
		expect(vi.mocked(fetch).mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
		flushSync();
	});
});
