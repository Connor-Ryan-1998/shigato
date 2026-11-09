import { beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { session } from '../../../lib/session.svelte';

const page = $state({ params: { org: 'acme' } });
vi.mock('$app/state', () => ({ get page() { return page; } }));
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock('../../../lib/api', () => ({ api: apiMock, ApiError: class ApiError extends Error {} }));
vi.mock('$app/navigation', () => ({ goto: vi.fn(), beforeNavigate: vi.fn() }));
import Page from './new/+page.svelte';
import ListPage from './+page.svelte';

const expense = { id: 7, spent_on: '2026-11-01', description: 'Office supplies', supplier_contact_id: 4, total: 1800, lines: [{ id: 1, category_id: 2, amount: 1800, gst_code: 'FRE', gst_amount: 0 }] };
function route(method: string, path: string, body?: unknown): Promise<unknown> {
	if (method === 'POST') return Promise.resolve({ ...expense, warnings: ['Server GST warning'] });
	if (path.endsWith('/categories')) return Promise.resolve([{ id: 2, name: 'Supplies', type: 'expense' }]);
	if (path.endsWith('/contacts?kind=supplier')) return Promise.resolve([{ id: 4, name: 'Supply shop' }]);
	if (path.includes('/expenses')) return Promise.resolve([expense]);
	throw new Error(`Unexpected ${method} ${path} ${body}`);
}
beforeEach(() => {
	page.params.org = 'acme';
	session.setMe({ user: { id: 1, email: 'owner@example.com', name: 'Owner' }, orgs: ['acme', 'other'].map((slug) => ({ slug, name: slug, role: 'owner', perms: ['reports_read', 'recon_write', 'invoices_read'] })) });
	session.setOrg('acme');
	session.setModules(['reconciliation']);
	apiMock.mockReset();
	apiMock.mockImplementation(route);
});
async function fill() {
	await fireEvent.input(await screen.findByLabelText('Description'), { target: { value: 'Cash supplies' } });
	await fireEvent.input(screen.getByLabelText('Amount ($)'), { target: { value: '18' } });
}

describe('Expenses', () => {
	it('rejects pending supplier options when contact permission is removed', async () => {
		let finish!: (value: unknown) => void;
		apiMock.mockImplementation((m: string, p: string) => p.endsWith('/contacts?kind=supplier') ? new Promise((resolve) => { finish = resolve; }) : route(m, p));
		render(Page);
		await vi.waitFor(() => expect(finish).toBeTypeOf('function'));
		const me = session.me!;
		session.setMe({ ...me, orgs: me.orgs.map((org) => ({ ...org, perms: org.perms.filter((perm) => perm !== 'invoices_read') })) });
		flushSync();
		finish([{ id: 4, name: 'Restricted supplier' }]);
		await screen.findByLabelText('Description');
		expect(screen.queryByRole('option', { name: 'Restricted supplier' })).toBeNull();
	});
	it('rejects pending project options after the module is disabled', async () => {
		let finish!: (value: unknown) => void;
		session.setModules(['reconciliation', 'timesheets']);
		apiMock.mockImplementation((m: string, p: string) => p.endsWith('/projects') ? new Promise((resolve) => { finish = resolve; }) : route(m, p));
		render(Page);
		await vi.waitFor(() => expect(finish).toBeTypeOf('function'));
		session.setModules(['reconciliation']);
		flushSync();
		finish([{ id: 44, name: 'Disabled project' }]);
		await screen.findByLabelText('Description');
		expect(screen.queryByLabelText('Project')).toBeNull();
		expect(screen.queryByRole('option', { name: 'Disabled project' })).toBeNull();
	});
	it('links to a dedicated creation screen instead of embedding the form', async () => {
		render(ListPage);
		await screen.findByRole('link', { name: 'Office supplies' });
		expect(screen.getByRole('link', { name: 'New expense' }).getAttribute('href')).toBe('/acme/expenses/new');
		expect(screen.queryByLabelText('Description')).toBeNull();
	});
	it('defaults and submits Brisbane local date rather than prior UTC day', async () => {
		const previousTZ = process.env.TZ;
		process.env.TZ = 'Australia/Brisbane';
		vi.setSystemTime(new Date('2026-11-08T22:00:00Z'));
		try {
			render(Page);
			const date = await screen.findByLabelText('Spent on');
			expect((date as HTMLInputElement).value).toBe('2026-11-09');
			await fill();
			await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
			expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/expenses', expect.objectContaining({ spent_on: '2026-11-09' }));
		} finally {
			vi.useRealTimers();
			if (previousTZ === undefined) delete process.env.TZ;
			else process.env.TZ = previousTZ;
		}
	});
	it('lists expenses with total, status and detail links', async () => {
		render(ListPage);
		const link = await screen.findByRole('link', { name: 'Office supplies' });
		expect(link.getAttribute('href')).toBe('/acme/expenses/7');
		expect(screen.getByText('$18.00')).toBeTruthy();
		expect(screen.getByText('Not matched to bank')).toBeTruthy();
	});
	it('filters inclusive dates via API and rejects reversed dates', async () => {
		render(ListPage);
		await screen.findByRole('link', { name: 'Office supplies' });
		await fireEvent.input(screen.getByLabelText('From', { exact: true }), { target: { value: '2026-11-01' } });
		await fireEvent.input(screen.getByLabelText('To', { exact: true }), { target: { value: '2026-11-01' } });
		await vi.waitFor(() => expect(apiMock).toHaveBeenCalledWith('GET', '/api/v1/orgs/acme/expenses?from=2026-11-01&to=2026-11-01'));
		await fireEvent.input(screen.getByLabelText('From', { exact: true }), { target: { value: '2026-11-02' } });
		await screen.findByText('From must not be after To.');
		expect(screen.queryByRole('link', { name: 'Office supplies' })).toBeNull();
	});
	it('saves standalone expense once, keeps warnings/link and resets only after success', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string, p: string) => m === 'POST' ? new Promise((resolve) => { finish = resolve; }) : route(m,p));
		render(Page);
		await fill();
		await fireEvent.change(screen.getByLabelText('Supplier'), { target: { value: '4' } });
		const save = screen.getByRole('button', { name: 'Save' });
		await fireEvent.click(save);
		await fireEvent.click(save);
		expect(apiMock.mock.calls.filter(([m]) => m === 'POST')).toHaveLength(1);
		expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/acme/expenses', expect.objectContaining({ description: 'Cash supplies', supplier_contact_id: 4, lines: [{ category_id: 2, amount: 1800 }] }));
		expect(screen.getByLabelText('Description').matches(':disabled')).toBe(true);
		finish({ ...expense, warnings: ['Server GST warning'] });
		await screen.findByText('Server GST warning');
		expect(screen.getByRole('link', { name: 'View saved expense' }).getAttribute('href')).toBe('/acme/expenses/7');
		expect((screen.getByLabelText('Description') as HTMLInputElement).value).toBe('');
	});
	it('keeps draft on API failure', async () => {
		apiMock.mockImplementation((m: string, p: string) => m === 'POST' ? Promise.reject(new Error('Failed')) : route(m,p));
		render(Page);
		await fill();
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		await screen.findByText('Could not save expense.');
		expect((screen.getByLabelText('Description') as HTMLInputElement).value).toBe('Cash supplies');
	});
	it('supports list and supporting-data retries without pretending errors are empty lists', async () => {
		apiMock.mockRejectedValueOnce(new Error('offline')).mockImplementation(route);
		render(ListPage);
		await screen.findByText('Could not load expenses.');
		expect(screen.queryByText('No expenses found.')).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		await screen.findByRole('link', { name: 'Office supplies' });
	});
	it('reports failed category load with retry, not a broken creation form', async () => {
		let failed = true;
		apiMock.mockImplementation((m: string,p: string) => failed && p.endsWith('/categories') ? Promise.reject(new Error('offline')) : route(m,p));
		render(Page);
		await screen.findByText('Could not load expense options.');
		expect(screen.queryByLabelText('Description')).toBeNull();
		failed = false;
		await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		await screen.findByLabelText('Description');
	});
	it('explains empty categories and empty expenses', async () => {
		apiMock.mockImplementation((m: string,p: string) => p.endsWith('/categories') || p.includes('/expenses') ? Promise.resolve([]) : route(m,p));
		render(ListPage);
		render(Page);
		await screen.findByText('No expenses found.');
		await screen.findByText('No expense categories available. Add an expense category before recording expenses.');
		expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
	});
	it('readers browse but cannot create', async () => {
		session.setMe({ user: { id: 1, email: 'book@example.com', name: 'Bookkeeper' }, orgs: [{ slug: 'acme', name: 'Acme', role: 'bookkeeper', perms: ['reports_read', 'invoices_read'] }] });
		render(ListPage);
		await screen.findByRole('link', { name: 'Office supplies' });
		expect(screen.queryByLabelText('Description')).toBeNull();
	});
	it('disabled module and mismatched org do not fetch or expose expense form', async () => {
		session.setModules([]);
		render(Page);
		await screen.findByText('Expenses are unavailable for this organisation.');
		expect(apiMock).not.toHaveBeenCalled();
	});
	it('ignores old filter results', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => p.endsWith('from=2026-11-01') ? new Promise((resolve) => { finish = resolve; }) : route(m,p));
		render(ListPage);
		await screen.findByRole('link', { name: 'Office supplies' });
		await fireEvent.input(screen.getByLabelText('From', { exact: true }), { target: { value: '2026-11-01' } });
		await fireEvent.input(screen.getByLabelText('From', { exact: true }), { target: { value: '2026-11-02' } });
		await screen.findByRole('link', { name: 'Office supplies' });
		finish([{ ...expense, description: 'Old filter' }]);
		await Promise.resolve(); flushSync();
		expect(screen.queryByRole('link', { name: 'Old filter' })).toBeNull();
	});
	it('ignores old save after org switch', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => m === 'POST' ? new Promise((resolve) => { finish = resolve; }) : route(m,p));
		render(Page);
		await fill();
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		page.params.org = 'other'; session.setOrg('other'); flushSync();
		await screen.findByLabelText('Description');
		finish({ ...expense, warnings: ['Old warning'] });
		await Promise.resolve(); flushSync();
		expect(screen.queryByText('Old warning')).toBeNull();
		expect(screen.queryByRole('link', { name: 'View saved expense' })).toBeNull();
		expect((screen.getByLabelText('Description') as HTMLInputElement).value).toBe('');
	});
	it('does not attach an old-org upload to the new draft', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => p.endsWith('/files') ? new Promise((resolve) => { finish = resolve; }) : route(m,p));
		render(Page);
		await fill();
		await fireEvent.change(screen.getByLabelText('Receipt'), { target: { files: [new File(['receipt'], 'old.pdf', { type: 'application/pdf' })] } });
		page.params.org = 'other'; session.setOrg('other'); flushSync();
		await screen.findByLabelText('Description');
		finish({ id: 88 }); await Promise.resolve(); flushSync();
		expect(screen.queryByText('Receipt attached.')).toBeNull();
		await fill();
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/other/expenses', expect.not.objectContaining({ receipt_file_id: 88 }));
	});
	it('cannot submit old-org form before effects run', async () => {
		render(Page);
		await fill();
		const save = screen.getByRole('button', { name: 'Save' });
		page.params.org = 'other';
		save.dispatchEvent(new MouseEvent('click', { bubbles: true }));
		expect(apiMock.mock.calls.filter(([m]) => m === 'POST')).toHaveLength(0);
		session.setOrg('other'); flushSync();
	});
	it('old-org options cannot seed a new-org expense', async () => {
		let finish!: (v: unknown) => void;
		apiMock.mockImplementation((m: string,p: string) => p === '/api/v1/orgs/acme/categories' ? new Promise((resolve) => { finish = resolve; }) : p === '/api/v1/orgs/other/categories' ? Promise.resolve([{ id: 3, name: 'New category', type: 'expense' }]) : route(m,p));
		render(Page);
		page.params.org = 'other'; session.setOrg('other'); flushSync();
		await screen.findByLabelText('Category');
		finish([{ id: 99, name: 'Old category', type: 'expense' }]); await Promise.resolve(); flushSync();
		expect(screen.queryByRole('option', { name: 'Old category' })).toBeNull();
		await fill();
		await fireEvent.click(screen.getByRole('button', { name: 'Save' }));
		expect(apiMock).toHaveBeenCalledWith('POST', '/api/v1/orgs/other/expenses', expect.objectContaining({ lines: [{ category_id: 3, amount: 1800 }] }));
	});
});
