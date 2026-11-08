import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

const page = $state({ params: { org: 'acme' } });
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { gotoMock } = vi.hoisted(() => ({ gotoMock: vi.fn() }));
vi.mock('$app/navigation', () => ({ goto: gotoMock }));
import Page from './+page.svelte';

function json(status: number, data: unknown) { return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } }); }
beforeEach(() => {
	page.params.org = 'acme';
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'owner', perms: ['invoices_read', 'invoices_write'] })) });
	session.setOrg('acme');
	gotoMock.mockReset();
	vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => Promise.resolve(init?.method === 'GET' ?
		json(200, [{ id: 2, name: 'Client Co', kind: 'client' }]) : json(422, { error: 'Issuing failed', invoice_id: 77 }))));
});
afterEach(() => vi.unstubAllGlobals());
async function form() {
	await screen.findByText('Client Co');
	return screen.getByRole('button', { name: 'Create invoice' }).closest('form')!;
}
describe('billing-run recovery', () => {
	it('links retained invoice and warns entries are already billed', async () => {
		render(Page);
		await fireEvent.submit(await form());
		const link = await screen.findByRole('link', { name: 'Open existing invoice' });
		expect(link.getAttribute('href')).toBe('/acme/invoices/77');
		expect(screen.getByText(/Time entries are already billed/)).toBeTruthy();
		expect(gotoMock).not.toHaveBeenCalled();
	});
	it('does not invent recovery link without valid invoice ID', async () => {
		vi.mocked(fetch).mockImplementation((_url, init) => Promise.resolve(init?.method === 'GET' ? json(200, [{ id: 2, name: 'Client Co', kind: 'client' }]) : json(422, { error: 'No unbilled entries' })));
		render(Page);
		await fireEvent.submit(await form());
		await screen.findByText('No unbilled entries');
		expect(screen.queryByRole('link', { name: 'Open existing invoice' })).toBeNull();
	});
	it('guards duplicate run and ignores old org recovery response', async () => {
		let finish!: (v: Response) => void;
		vi.mocked(fetch).mockImplementation((_url, init) => init?.method === 'GET' ? Promise.resolve(json(200, [{ id: 2, name: 'Client Co', kind: 'client' }])) : new Promise((resolve) => { finish = resolve; }));
		render(Page);
		const runForm = await form();
		await fireEvent.submit(runForm);
		await fireEvent.submit(runForm);
		expect(vi.mocked(fetch).mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
		page.params.org = 'other';
		session.setOrg('other');
		flushSync();
		await screen.findByText('Client Co');
		finish(json(422, { error: 'Old error', invoice_id: 77 }));
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
