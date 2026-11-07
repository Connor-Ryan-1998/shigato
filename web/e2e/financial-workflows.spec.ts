import { test, expect, type APIRequestContext } from '@playwright/test';
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const root = resolve(import.meta.dirname, '../..');
const dbName = 'shigoto_e2e_financial';
const email = 'finance-owner@example.com';
const password = 'financial-e2e-password';
const today = new Date().toISOString().slice(0, 10);
const apiBase = '/api/v1/orgs/finance';
let base = '';
let filesDir = '';
let server: ChildProcess | undefined;

test.use({ timezoneId: 'UTC' });

function psql(sql: string, database = dbName) {
	return execFileSync('docker', ['compose', 'exec', '-T', 'db', 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'shigoto', '-d', database, '-c', sql], { cwd: root, encoding: 'utf8' });
}

async function api<T>(request: APIRequestContext, method: string, path: string, data?: unknown): Promise<T> {
	const res = await request.fetch(`${base}${apiBase}${path}`, {
		method, data, headers: { 'X-Shigoto-CSRF': '1' }
	});
	expect(res.ok(), `${method} ${path}: ${await res.text()}`).toBeTruthy();
	return res.json();
}

type Contact = { id: number; kind: string };
type Invoice = { id: number; number: number; status: string; total: number; lines: { description: string }[]; payments: { amount: number }[] };

test.beforeAll(async () => {
	execFileSync('docker', ['compose', 'up', '-d', '--wait', 'db'], { cwd: root, stdio: 'inherit' });
	psql(`DROP DATABASE IF EXISTS ${dbName} WITH (FORCE)`, 'shigoto');
	psql(`CREATE DATABASE ${dbName}`, 'shigoto');
	const socket = createServer();
	socket.listen(0, '127.0.0.1');
	await once(socket, 'listening');
	const address = socket.address();
	if (!address || typeof address === 'string') throw new Error('missing port');
	base = `http://127.0.0.1:${address.port}`;
	await new Promise<void>((ok, fail) => socket.close((err) => err ? fail(err) : ok()));
	filesDir = mkdtempSync(join(tmpdir(), 'shigoto-e2e-financial-'));
	const env = {
		...process.env,
		SHIGOTO_DATABASE_URL: `postgres://shigoto:shigoto@localhost:5432/${dbName}?sslmode=disable`,
		SHIGOTO_BASE_URL: base, SHIGOTO_LISTEN: base.replace('http://', ''),
		SHIGOTO_FILES_DIR: filesDir, SHIGOTO_SCHEDULER: 'off', SHIGOTO_TIMEZONE: 'UTC',
		SHIGOTO_MODULES: 'property,timesheets,reconciliation', SHIGOTO_SMTP_HOST: '',
		SHIGOTO_OIDC_ISSUER: '', SHIGOTO_OIDC_CLIENT_ID: '', SHIGOTO_OIDC_CLIENT_SECRET: '',
		SHIGOTO_STRIPE_FINANCE_SECRET_KEY: '', SHIGOTO_STRIPE_FINANCE_WEBHOOK_SECRET: ''
	};
	const binary = join(root, 'bin/shigoto');
	execFileSync(binary, ['admin', 'create-owner', '--org-slug', 'finance', '--org-name', 'Finance tests', '--email', email], { env, input: `${password}\n` });
	psql(`INSERT INTO contacts (org_id,kind,name) SELECT id,'client','Finance client' FROM orgs;
		INSERT INTO contacts (org_id,kind,name) SELECT id,'tenant','Finance tenant' FROM orgs;`);
	server = spawn(binary, ['serve'], { env, stdio: 'inherit' });
	await expect.poll(async () => {
		try { return (await fetch(`${base}/healthz`)).ok; } catch { return false; }
	}, { timeout: 10_000 }).toBe(true);
});

test.afterAll(async () => {
	if (server && server.exitCode === null && server.signalCode === null) {
		const exited = once(server, 'exit');
		server.kill();
		await exited;
	}
	if (filesDir) rmSync(filesDir, { recursive: true, force: true });
});

test.beforeEach(async ({ page }) => {
	await page.goto(`${base}/login`);
	await page.getByLabel('Email', { exact: true }).fill(email);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).toHaveURL(`${base}/finance`);
});

test('invoice survives session expiry and is issued exactly once', async ({ page }) => {
	const before = await api<{ total: number }>(page.request, 'GET', '/invoices');
	await page.goto(`${base}/finance/invoices/new`);
	await page.getByLabel('Description', { exact: true }).fill('Retained financial work');
	await page.getByLabel('Quantity', { exact: true }).fill('2');
	await page.getByLabel('Unit price ($)', { exact: true }).fill('75');
	await page.getByLabel('GST code', { exact: true }).selectOption('FRE');
	const cookie = (await page.context().cookies()).find((c) => c.name === 'shigoto_session');
	expect(cookie).toBeDefined();
	const hash = createHash('sha256').update(cookie!.value).digest('hex');
	psql(`UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=decode('${hash}','hex')`);
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	const dialog = page.getByRole('dialog', { name: 'Session expired' });
	await expect(dialog).toBeVisible();
	await expect(page.getByLabel('Description', { exact: true })).toHaveValue('Retained financial work');
	await dialog.getByLabel('Password').fill(password);
	await dialog.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).toHaveURL(new RegExp(`${base}/finance/invoices/\\d+$`));
	const id = page.url().split('/').at(-1);
	await expect(page.getByText('Retained financial work', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Issue', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Record payment', exact: true })).toBeVisible();
	const inv = await api<Invoice>(page.request, 'GET', `/invoices/${id}`);
	expect(inv).toMatchObject({ status: 'open', total: 15000 });
	const listed = await api<{ total: number }>(page.request, 'GET', '/invoices');
	expect(listed.total).toBe(before.total + 1);
});

test('logged time bills once and locks its entry', async ({ page }) => {
	const contacts = await api<Contact[]>(page.request, 'GET', '/contacts');
	const client = contacts.find((c) => c.kind === 'client')!;
	const project = await api<{ id: number }>(page.request, 'POST', '/projects', { name: 'Financial work', client_contact_id: client.id, hourly_rate: 12000 });
	await api(page.request, 'POST', `/projects/${project.id}/tasks`, { name: 'Consulting' });
	await page.goto(`${base}/finance/time`);
	await page.getByLabel('Project', { exact: true }).selectOption(String(project.id));
	await expect(page.getByLabel('Task', { exact: true })).toBeVisible();
	await page.getByLabel('Date', { exact: true }).fill(today);
	await page.getByLabel('Minutes', { exact: true }).fill('50');
	await page.getByLabel('Note', { exact: true }).fill('Bill this once');
	await page.getByRole('button', { name: 'Log time', exact: true }).click();
	await expect(page.getByRole('cell', { name: 'Bill this once', exact: true })).toBeVisible();
	await page.goto(`${base}/finance/billing-run`);
	await page.getByLabel('Client', { exact: true }).selectOption(String(client.id));
	await page.getByLabel('From', { exact: true }).fill(today);
	await page.getByLabel('To', { exact: true }).fill(today);
	await page.getByRole('button', { name: 'Create invoice', exact: true }).click();
	await expect(page).toHaveURL(new RegExp(`${base}/finance/invoices/\\d+$`));
	const inv = await api<Invoice>(page.request, 'GET', `/invoices/${page.url().split('/').at(-1)}`);
	expect(inv).toMatchObject({ status: 'draft', total: 10000 });
	const beforeRetry = await api<{ total: number }>(page.request, 'GET', '/invoices');
	const duplicate = await page.request.post(`${base}${apiBase}/billing-runs`, { headers: { 'X-Shigoto-CSRF': '1' }, data: { client_contact_id: client.id, from: today, to: today, due_in_days: 14 } });
	expect(duplicate.status()).toBe(422);
	expect((await api<{ total: number }>(page.request, 'GET', '/invoices')).total).toBe(beforeRetry.total);
	await page.goto(`${base}/finance/time`);
	await expect(page.getByRole('row').filter({ hasText: 'Bill this once' })).toContainText('Billed');
	const entries = await api<{ entries: { id: number; invoice_line_id: number }[] }>(page.request, 'GET', `/time-entries?project_id=${project.id}`);
	expect(entries.entries).toHaveLength(1);
	expect(entries.entries[0].invoice_line_id).toBeGreaterThan(0);
	const deleted = await page.request.delete(`${base}${apiBase}/time-entries/${entries.entries[0].id}`, { headers: { 'X-Shigoto-CSRF': '1' } });
	expect(deleted.status()).toBe(409);
});

test('property and lease persist and expose their ledger', async ({ page }) => {
	await page.goto(`${base}/finance/properties`);
	await page.getByLabel('Name', { exact: true }).fill('Finance cottage');
	await page.getByRole('button', { name: 'Create property', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Finance cottage' })).toBeVisible();
	await page.getByLabel('Start', { exact: true }).fill(today);
	await page.getByLabel('Rent ($)', { exact: true }).fill('500');
	await page.getByRole('button', { name: 'Create lease', exact: true }).click();
	await page.getByRole('link', { name: `Lease from ${today}`, exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Ledger', exact: true })).toBeVisible();
	const id = page.url().split('/').at(-1);
	const lease = await api<{ rent_changes: { amount: number }[] }>(page.request, 'GET', `/leases/${id}`);
	expect(lease.rent_changes[0].amount).toBe(50000);
	await api(page.request, 'PUT', '/settings', { name: 'Finance tests', invoice_lead_days: 0, auto_send_rent: false });
	await api(page.request, 'POST', '/rent-run');
	await api(page.request, 'POST', '/rent-run');
	const ledger = await api<{ unissued: number }>(page.request, 'GET', `/leases/${id}/ledger`);
	expect(ledger.unissued).toBe(50000);
});

test('bank credit pays an invoice and reaches report and CSV', async ({ page }) => {
	const contacts = await api<Contact[]>(page.request, 'GET', '/contacts');
	const client = contacts.find((c) => c.kind === 'client')!;
	const draft = await api<Invoice>(page.request, 'POST', '/invoices', { contact_id: client.id, source: 'manual', due_in_days: 14, lines: [{ description: 'Reconciled work', quantity_milli: 1000, unit_amount: 15000, gst_code: 'FRE' }] });
	const inv = await api<Invoice>(page.request, 'POST', `/invoices/${draft.id}/issue`);
	const account = await api<{ id: number }>(page.request, 'POST', '/bank-accounts', { name: 'Financial test account', csv_mapping: { has_header: true, date_column: 0, date_format: '2006-01-02', amount_column: 1, description_column: 2 } });
	const imported = await page.request.post(`${base}${apiBase}/bank-accounts/${account.id}/imports`, { headers: { 'X-Shigoto-CSRF': '1' }, multipart: { file: { name: 'credit.csv', mimeType: 'text/csv', buffer: Buffer.from(`Date,Amount,Description\n${today},150.00,Financial credit\n`) } } });
	expect(imported.ok(), await imported.text()).toBeTruthy();
	await page.goto(`${base}/finance/reconciliation?account_id=${account.id}`);
	await page.getByRole('row').filter({ hasText: 'Financial credit' }).getByRole('button', { name: 'View', exact: true }).click();
	await page.getByRole('listitem').filter({ hasText: `#${inv.number} —` }).getByRole('button', { name: 'Match', exact: true }).click();
	await expect(page.getByRole('dialog', { name: 'Bank line details' })).not.toBeVisible();
	const paid = await api<Invoice>(page.request, 'GET', `/invoices/${inv.id}`);
	expect(paid).toMatchObject({ status: 'paid', payments: [{ amount: 15000 }] });
	await page.goto(`${base}/finance/reports`);
	await page.getByLabel('From', { exact: true }).fill(today);
	await page.getByLabel('To', { exact: true }).fill(today);
	await expect(page.getByRole('row').filter({ hasText: 'Total income' })).toContainText('150.00');
	const href = await page.getByRole('link', { name: 'Transactions CSV', exact: true }).getAttribute('href');
	const csv = await page.request.get(`${base}${href}`);
	expect(csv.ok()).toBeTruthy();
	expect(await csv.text()).toContain(`${today},income,Other income,FRE,150.00,0.00,150.00`);
});
