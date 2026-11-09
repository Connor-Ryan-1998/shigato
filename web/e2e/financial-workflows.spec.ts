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

test('contact details can be edited, cleared and reloaded without replacing identity', async ({ page }) => {
	const original = `Contact ${crypto.randomUUID()}`;
	const corrected = `Corrected ${crypto.randomUUID()}`;
	const contact = await api<Contact>(page.request, 'POST', '/contacts', { kind: 'supplier', name: original, email: 'old@example.com', phone: '0400123456', company_name: 'Old Co', abn: '51824753556' });
	await page.goto(`${base}/finance/contacts`);
	await page.getByRole('button', { name: `Edit ${original}`, exact: true }).click();
	await expect(page.getByLabel('Name', { exact: true })).toHaveValue(original);
	await page.getByLabel('Name', { exact: true }).fill(corrected);
	await page.getByLabel('Email', { exact: true }).fill('corrected@example.com');
	for (const label of ['Phone', 'Company name', 'ABN']) await page.getByLabel(label, { exact: true }).fill('');
	await page.getByRole('button', { name: 'Save contact', exact: true }).click();
	await expect(page.getByText('Contact updated.', { exact: true })).toBeVisible();
	await expect(page.getByRole('row').filter({ hasText: corrected })).toContainText('corrected@example.com');
	await page.reload();
	await page.getByRole('button', { name: `Edit ${corrected}`, exact: true }).click();
	await expect(page.getByLabel('Email', { exact: true })).toHaveValue('corrected@example.com');
	await expect(page.getByLabel('Phone', { exact: true })).toHaveValue('');
	await page.getByLabel('Name', { exact: true }).fill('Discard this edit');
	page.once('dialog', (dialog) => dialog.accept());
	await page.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(page.getByRole('cell', { name: corrected, exact: true })).toBeVisible();
	const stored = (await api<(Contact & { name: string; email?: string; phone?: string; company_name?: string; abn?: string })[]>(page.request, 'GET', '/contacts')).find((c) => c.id === contact.id)!;
	expect(stored).toMatchObject({ id: contact.id, kind: 'supplier', name: corrected, email: 'corrected@example.com' });
	expect(stored.phone ?? '').toBe('');
	expect(stored.company_name ?? '').toBe('');
	expect(stored.abn ?? '').toBe('');
});

test('standalone expense splits and receipt persist without a bank import', async ({ page }) => {
	const suffix = crypto.randomUUID();
	const description = `Cash supplies ${suffix}`;
	const category = await api<{ id: number }>(page.request, 'POST', '/categories', { name: `Supplies ${suffix}`, type: 'expense', default_gst_code: 'FRE', capital: false });
	const supplier = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'supplier', name: `Shop ${suffix}` });
	const property = await api<{ id: number }>(page.request, 'POST', '/properties', { name: `Expense cottage ${suffix}`, residential: true });
	await page.getByRole('link', { name: 'Expenses', exact: true }).click();
	await expect(page).toHaveURL(`${base}/finance/expenses`);
	await page.getByRole('link', { name: 'New expense', exact: true }).click();
	await page.getByLabel('Spent on', { exact: true }).fill(today);
	await page.getByLabel('Description', { exact: true }).fill(description);
	await page.getByLabel('Supplier', { exact: true }).selectOption(String(supplier.id));
	await page.getByLabel('Category', { exact: true }).selectOption(String(category.id));
	await page.getByLabel('Amount ($)', { exact: true }).fill('12.34');
	await page.getByLabel('Property', { exact: true }).selectOption(String(property.id));
	await page.getByRole('button', { name: 'Add line', exact: true }).click();
	await page.getByLabel('Category', { exact: true }).nth(1).selectOption(String(category.id));
	await page.getByLabel('Amount ($)', { exact: true }).nth(1).fill('5.66');
	const receipt = Buffer.from('%PDF-1.4\n%Shigoto expense receipt\n%%EOF\n');
	await page.getByLabel('Receipt', { exact: true }).setInputFiles({ name: 'cash-receipt.pdf', mimeType: 'application/pdf', buffer: receipt });
	await expect(page.getByText('Receipt attached.', { exact: true })).toBeVisible();
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	await expect(page.getByText('residential property expenses are input-taxed; GST credit not claimed', { exact: true })).toBeVisible();
	const savedLink = page.getByRole('link', { name: 'View saved expense', exact: true });
	const savedPath = await savedLink.getAttribute('href');
	const id = savedPath!.split('/').at(-1);
	const stored = await api<{ total: number; spent_on: string; supplier_contact_id: number; receipt_file_id: number; bank_line_id?: number; lines: { category_id: number; amount: number; gst_code: string; gst_amount: number; property_id?: number }[] }>(page.request, 'GET', `/expenses/${id}`);
	expect(stored).toMatchObject({ spent_on: today, supplier_contact_id: supplier.id, total: 1800 });
	expect(stored.bank_line_id).toBeUndefined();
	expect(stored.receipt_file_id).toBeGreaterThan(0);
	expect(stored.lines).toMatchObject([{ category_id: category.id, amount: 1234, gst_code: 'INP', gst_amount: 0, property_id: property.id }, { category_id: category.id, amount: 566, gst_code: 'FRE', gst_amount: 0 }]);
	await page.getByRole('link', { name: 'Back to expenses', exact: true }).click();
	await page.getByLabel('From', { exact: true }).fill(today);
	await page.getByLabel('To', { exact: true }).fill(today);
	await expect(page.getByRole('link', { name: description, exact: true })).toBeVisible();
	const next = new Date(`${today}T00:00:00Z`);
	next.setUTCDate(next.getUTCDate() + 1);
	await page.getByLabel('From', { exact: true }).fill(next.toISOString().slice(0, 10));
	await expect(page.getByText('From must not be after To.', { exact: true })).toBeVisible();
	await page.getByLabel('To', { exact: true }).fill(next.toISOString().slice(0, 10));
	await expect(page.getByText('No expenses found.', { exact: true })).toBeVisible();
	await page.goto(`${base}${savedPath}`);
	await expect(page).toHaveURL(`${base}${savedPath}`);
	await expect(page.getByRole('heading', { name: description, exact: true })).toBeVisible();
	await page.reload();
	await expect(page.getByRole('heading', { name: description, exact: true })).toBeVisible();
	await expect(page.getByRole('cell', { name: `Supplies ${suffix}`, exact: true })).toHaveCount(2);
	await expect(page.getByText('Not matched to bank', { exact: true })).toBeVisible();
	const downloadLink = page.getByRole('link', { name: 'Download receipt', exact: true });
	const filePath = await downloadLink.getAttribute('href');
	const file = await page.request.get(`${base}${filePath}`);
	expect(file.ok()).toBeTruthy();
	expect(await file.body()).toEqual(receipt);
	const downloadEvent = page.waitForEvent('download');
	await downloadLink.click();
	const download = await downloadEvent;
	expect(download.suggestedFilename()).toBe('cash-receipt.pdf');
	expect(await download.failure()).toBeNull();
});

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

test('shared dialogs protect edits, recover from errors, and restore keyboard focus', async ({ page }) => {
	await page.goto(`${base}/finance/contacts`);
	const launch = page.getByRole('button', { name: 'New contact', exact: true });
	await launch.click();
	const editor = page.getByRole('dialog', { name: 'New contact', exact: true });
	await expect(editor.getByLabel('Contact kind', { exact: true })).toBeFocused();
	await editor.getByRole('button', { name: 'Create contact', exact: true }).focus();
	await page.keyboard.press('Tab');
	// Chromium can visit browser chrome between the last and first modal control.
	const focus = await page.evaluate(() => ({ modal: !!document.activeElement?.closest('dialog'), browser: document.activeElement === document.body }));
	expect(focus.modal || focus.browser).toBe(true);
	if (focus.browser) await page.keyboard.press('Tab');
	await expect(editor.getByLabel('Contact kind', { exact: true })).toBeFocused();
	await editor.getByLabel('Name', { exact: true }).fill('Protected draft');
	page.once('dialog', (dialog) => dialog.dismiss());
	await page.keyboard.press('Escape');
	await expect(editor).toBeVisible();
	await expect(editor.getByLabel('Name', { exact: true })).toHaveValue('Protected draft');
	await page.route('**/api/v1/orgs/finance/contacts', async (route) => {
		if (route.request().method() === 'POST') await route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: 'Simulated contact rejection' }) });
		else await route.continue();
	});
	await editor.getByRole('button', { name: 'Create contact', exact: true }).click();
	await expect(editor.getByRole('alert')).toHaveText('Simulated contact rejection');
	await expect(editor.getByLabel('Name', { exact: true })).toHaveValue('Protected draft');
	page.once('dialog', (dialog) => dialog.accept());
	await editor.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(editor).not.toBeVisible();
	await expect(launch).toBeFocused();
});

test('cancelling expired-session sign-in leaves a pending draft without another prompt', async ({ page }) => {
	await page.goto(`${base}/finance/invoices/new`);
	await page.getByLabel('Description', { exact: true }).fill('Expired draft');
	await page.getByLabel('Quantity', { exact: true }).fill('1');
	await page.getByLabel('Unit price ($)', { exact: true }).fill('10');
	const cookie = (await page.context().cookies()).find((c) => c.name === 'shigoto_session')!;
	const hash = createHash('sha256').update(cookie.value).digest('hex');
	psql(`UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=decode('${hash}','hex')`);
	await page.getByRole('button', { name: 'Save', exact: true }).click();
	const prompts: string[] = [];
	page.on('dialog', async (dialog) => { prompts.push(dialog.type()); await dialog.dismiss(); });
	await page.getByRole('dialog', { name: 'Session expired' }).getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(page).toHaveURL(new RegExp(`${base}/login(?:\\?|$)`));
	expect(prompts).toEqual([]);
});

test('page editors protect navigation and settings reset to saved values', async ({ page }) => {
	await page.goto(`${base}/finance/settings`);
	const name = page.getByLabel('Organisation name', { exact: true });
	const original = await name.inputValue();
	await name.fill('Unsaved organisation');
	const discard = page.waitForEvent('dialog');
	const navigate = page.getByRole('link', { name: 'Contacts', exact: true }).click();
	await (await discard).dismiss();
	await navigate;
	await expect(page).toHaveURL(`${base}/finance/settings`);
	await expect(name).toHaveValue('Unsaved organisation');
	page.once('dialog', (dialog) => dialog.accept());
	await page.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(name).toHaveValue(original);
	await page.getByRole('link', { name: 'Invoices', exact: true }).click();
	await page.getByRole('button', { name: 'New invoice', exact: true }).click();
	await page.getByLabel('Description', { exact: true }).fill('Unsaved invoice');
	await expect(page.getByLabel('Due in days', { exact: true })).toHaveAttribute('aria-describedby', 'due-in-days-hint');
	page.once('dialog', (dialog) => dialog.dismiss());
	await page.goBack();
	await expect(page).toHaveURL(`${base}/finance/invoices/new`);
	await expect(page.getByLabel('Description', { exact: true })).toHaveValue('Unsaved invoice');
	page.once('dialog', (dialog) => dialog.accept());
	await page.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expect(page).toHaveURL(`${base}/finance/invoices`);
});

test('staff lists stay within narrow screens in both themes', async ({ page }) => {
	await page.setViewportSize({ width: 375, height: 812 });
	for (const colorScheme of ['light', 'dark'] as const) {
		await page.emulateMedia({ colorScheme });
		for (const path of ['contacts', 'properties', 'time', 'expenses', 'settings']) {
			await page.goto(`${base}/finance/${path}`);
			await expect(page.locator('main h1')).toHaveCount(1);
			await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
		}
	}
});

test('logged time bills once and locks its entry', async ({ page }) => {
	const contacts = await api<Contact[]>(page.request, 'GET', '/contacts');
	const client = contacts.find((c) => c.kind === 'client')!;
	const project = await api<{ id: number }>(page.request, 'POST', '/projects', { name: 'Financial work', client_contact_id: client.id, hourly_rate: 12000 });
	await api(page.request, 'POST', `/projects/${project.id}/tasks`, { name: 'Consulting' });
	await page.getByRole('link', { name: 'Time-Logging', exact: true }).click();
	await expect(page).toHaveURL(`${base}/finance/time`);
	await expect(page.getByRole('heading', { name: 'Timesheets Yet to be Invoiced', exact: true })).toBeVisible();
	await page.getByRole('button', { name: '+ New Log Entry', exact: true }).click();
	const dialog = page.getByRole('dialog', { name: 'Log time' });
	await dialog.getByLabel('Project', { exact: true }).selectOption(String(project.id));
	await expect(dialog.getByRole('option', { name: 'Consulting', exact: true })).toBeAttached();
	await dialog.getByLabel('Date', { exact: true }).fill(today);
	await dialog.getByLabel('Time (H:MM)', { exact: true }).fill('0:50');
	await dialog.getByLabel('Note', { exact: true }).fill('Bill this once');
	await dialog.getByRole('button', { name: 'Log time', exact: true }).click();
	await expect(page.getByRole('row').filter({ hasText: 'Bill this once' })).toContainText('Unbilled');
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
	await expect(page.getByRole('heading', { name: 'Timesheets Yet to be Invoiced', exact: true })).toBeVisible();
	await expect(page.getByRole('row').filter({ hasText: 'Bill this once' })).toHaveCount(0);
	await page.getByRole('button', { name: 'Timesheets Yet to be Invoiced' }).click();
	await page.getByRole('button', { name: 'Timesheets Invoiced', exact: true }).click();
	await expect(page.getByRole('row').filter({ hasText: 'Bill this once' })).toContainText('Invoiced');
	const entries = await api<{ entries: { id: number; invoice_line_id: number }[] }>(page.request, 'GET', `/time-entries?project_id=${project.id}`);
	expect(entries.entries).toHaveLength(1);
	expect(entries.entries[0].invoice_line_id).toBeGreaterThan(0);
	const deleted = await page.request.delete(`${base}${apiBase}/time-entries/${entries.entries[0].id}`, { headers: { 'X-Shigoto-CSRF': '1' } });
	expect(deleted.status()).toBe(409);
});

test('property and lease persist and expose their ledger', async ({ page }) => {
	await page.goto(`${base}/finance/properties`);
	await page.getByRole('button', { name: 'New property', exact: true }).click();
	await page.getByLabel('Name', { exact: true }).fill('Finance cottage');
	await page.getByRole('button', { name: 'Create property', exact: true }).click();
	await expect(page.getByRole('cell', { name: 'Finance cottage', exact: true })).toBeVisible();
	await page.getByRole('link', { name: 'New lease', exact: true }).click();
	await page.getByLabel('Start', { exact: true }).fill(today);
	await page.getByLabel('Rent ($)', { exact: true }).fill('500');
	await page.getByRole('button', { name: 'Create lease', exact: true }).click();
	await page.getByRole('link', { name: 'View saved lease', exact: true }).click();
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

test('owner updates settings and payment instructions persist after reload', async ({ page }) => {
	const original = await api<Record<string, unknown>>(page.request, 'GET', '/settings');
	expect(original.auto_send_rent).toBe(false);
	try {
		await page.getByRole('link', { name: 'Settings', exact: true }).click();
		await page.getByLabel('Organisation name', { exact: true }).fill('Updated finance org');
		await page.getByLabel('BSB', { exact: true }).fill('001002');
		await page.getByLabel('Bank account', { exact: true }).fill('00123456');
		await page.getByLabel('PayID', { exact: true }).fill('finance@example.com');
		await page.getByRole('button', { name: 'Save settings', exact: true }).click();
		await expect(page.getByRole('status')).toHaveText('Settings saved.');
		await expect(page.getByLabel('Organisation', { exact: true }).locator('option:checked')).toHaveText('Updated finance org');
		await page.reload();
		await expect(page.getByLabel('Organisation name', { exact: true })).toHaveValue('Updated finance org');
		await expect(page.getByLabel('BSB', { exact: true })).toHaveValue('001-002');
		await expect(page.getByLabel('Bank account', { exact: true })).toHaveValue('00123456');
		await expect(page.getByLabel('PayID', { exact: true })).toHaveValue('finance@example.com');
		expect(await api(page.request, 'GET', '/settings')).toEqual({
			...original, name: 'Updated finance org', bank_bsb: '001-002',
			bank_account: '00123456', payid: 'finance@example.com'
		});
	} finally {
		await api(page.request, 'PUT', '/settings', original);
	}
});

test('new contacts persist, filter and appear in lease and project forms', async ({ page }) => {
	const suffix = crypto.randomUUID();
	const created: Record<string, string> = {};
	await page.getByRole('link', { name: 'Contacts', exact: true }).click();
	for (const kind of ['tenant', 'client', 'supplier']) {
		const name = `UI ${kind} ${suffix}`;
		created[kind] = name;
		await page.getByRole('button', { name: 'New contact', exact: true }).click();
		await page.getByLabel('Contact kind', { exact: true }).selectOption(kind);
		await page.getByLabel('Name', { exact: true }).fill(name);
		await page.getByLabel('Email', { exact: true }).fill(`${kind}-${suffix}@example.com`);
		await page.getByRole('button', { name: 'Create contact', exact: true }).click();
		await expect(page.getByRole('cell', { name, exact: true })).toBeVisible();
	}
	await page.reload();
	for (const kind of ['tenant', 'client', 'supplier']) {
		await page.getByLabel('Filter by kind', { exact: true }).selectOption(kind);
		await expect(page.getByRole('cell', { name: created[kind], exact: true })).toBeVisible();
		for (const other of ['tenant', 'client', 'supplier'].filter((k) => k !== kind)) {
			await expect(page.getByRole('cell', { name: created[other], exact: true })).toHaveCount(0);
		}
	}
	const contacts = await api<{ id: number; name: string; kind: string }[]>(page.request, 'GET', '/contacts');
	for (const [kind, name] of Object.entries(created)) {
		expect(contacts.find((c) => c.name === name)).toMatchObject({ kind });
	}
	await api(page.request, 'POST', '/properties', { name: `Contact fixture ${suffix}`, residential: true });
	await page.goto(`${base}/finance/leases/new`);
	const tenant = contacts.find((c) => c.name === created.tenant)!;
	await page.getByLabel('Tenant', { exact: true }).selectOption(String(tenant.id));
	await expect(page.getByLabel('Tenant', { exact: true })).toHaveValue(String(tenant.id));
	page.once('dialog', (dialog) => dialog.accept());
	await page.getByRole('link', { name: 'Projects', exact: true }).click();
	await page.getByRole('button', { name: 'New project' }).click();
	const client = contacts.find((c) => c.name === created.client)!;
	await page.getByLabel('Customer', { exact: true }).selectOption(String(client.id));
	await expect(page.getByLabel('Customer', { exact: true })).toHaveValue(String(client.id));
});

test('time entry edits persist and date filters update totals', async ({ page }) => {
	const client = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'client', name: `Time edit ${crypto.randomUUID()}` });
	const project = await api<{ id: number }>(page.request, 'POST', '/projects', { name: `Time edit ${crypto.randomUUID()}`, client_contact_id: client.id, hourly_rate: 12000 });
	const task = await api<{ id: number }>(page.request, 'POST', `/projects/${project.id}/tasks`, { name: 'Edit task' });
	const entry = await api<{ id: number }>(page.request, 'POST', '/time-entries', { task_id: task.id, work_date: today, minutes: 30, note: 'Before edit' });
	const alternative = await api<{ id: number }>(page.request, 'POST', `/projects/${project.id}/tasks`, { name: 'Alternative task' });
	await api(page.request, 'PUT', `/tasks/${task.id}`, { name: 'Edit task', hourly_rate: null, active: false });
	await page.goto(`${base}/finance/time`);
	await page.getByLabel('Project', { exact: true }).selectOption(String(project.id));
	await page.getByRole('button', { name: `Edit entry ${entry.id}`, exact: true }).click();
	const dialog = page.getByRole('dialog', { name: 'Edit time entry' });
	await dialog.getByLabel('Time (H:MM)', { exact: true }).fill('1:15');
	await dialog.getByLabel('Note', { exact: true }).fill('After edit');
	await dialog.getByLabel('Task', { exact: true }).selectOption(String(alternative.id));
	await dialog.getByLabel('Task', { exact: true }).selectOption(String(task.id));
	await expect(dialog.getByLabel('Note', { exact: true })).toHaveValue('After edit');
	await dialog.getByRole('button', { name: 'Save entry', exact: true }).click();
	await expect(page.getByText('After edit', { exact: true })).toBeVisible();
	const saved = await api<{ entries: { id: number; task_id: number; minutes: number; note: string }[] }>(page.request, 'GET', `/time-entries?project_id=${project.id}`);
	expect(saved.entries).toEqual([expect.objectContaining({ id: entry.id, task_id: task.id, minutes: 75, note: 'After edit' })]);
	await page.getByLabel('Period', { exact: true }).selectOption('this_week');
	await page.getByLabel('Project', { exact: true }).selectOption(String(project.id));
	await expect(page.getByRole('row', { name: /^Total/ })).toContainText('01:15');
	await page.getByLabel('Period', { exact: true }).selectOption('last_month');
	await expect(page.getByText('No time logged', { exact: true })).toBeVisible();
});

test('project and task rates edit and archived project restores', async ({ page }) => {
	const suffix = crypto.randomUUID();
	const client = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'client', name: `Rates ${suffix}` });
	const project = await api<{ id: number }>(page.request, 'POST', '/projects', { name: `Rates ${suffix}`, client_contact_id: client.id, hourly_rate: 12000 });
	await api(page.request, 'POST', `/projects/${project.id}/tasks`, { name: 'Rate task' });
	const getProject = () => api<Record<string, unknown>>(page.request, 'GET', `/projects/${project.id}`);
	const firstTask = async () => (await api<Record<string, unknown>[]>(page.request, 'GET', `/projects/${project.id}/tasks`))[0];
	const dialog = page.getByRole('dialog');

	await page.goto(`${base}/finance/projects`);
	await page.getByLabel('Search projects').fill(suffix);
	await page.getByRole('link', { name: `Rates ${suffix}`, exact: true }).click();

	await page.getByRole('button', { name: 'Edit', exact: true }).click();
	await dialog.getByLabel('Project name', { exact: true }).fill(`Renamed ${suffix}`);
	await dialog.getByLabel('Base rate ($/h)', { exact: true }).fill('175.50');
	await dialog.getByLabel('Description', { exact: true }).fill('Rate review');
	await dialog.getByRole('button', { name: 'Save project', exact: true }).click();
	await expect(dialog).toBeHidden();
	await expect(page.getByRole('heading', { name: `Renamed ${suffix}`, exact: true })).toBeVisible();
	expect(await getProject()).toMatchObject({ name: `Renamed ${suffix}`, hourly_rate: 17550, gst_code: 'GST', description: 'Rate review', status: 'in_progress', active: true });

	await page.getByRole('button', { name: 'Edit task Rate task', exact: true }).click();
	await dialog.getByLabel('Task name', { exact: true }).fill('Updated rate task');
	await dialog.getByLabel('Task hourly rate ($)', { exact: true }).fill('125.50');
	await dialog.getByRole('button', { name: 'Save task', exact: true }).click();
	await expect(dialog).toBeHidden();
	await expect.poll(async () => (await firstTask()).hourly_rate).toBe(12550);
	await page.getByRole('button', { name: 'Edit task Updated rate task', exact: true }).click();
	await dialog.getByLabel('Task hourly rate ($)', { exact: true }).fill('');
	await dialog.getByRole('button', { name: 'Save task', exact: true }).click();
	await expect(dialog).toBeHidden();
	await expect.poll(async () => firstTask()).not.toHaveProperty('hourly_rate');

	// Completing the project closes it to time logging; it leaves the default (active) list.
	await page.getByRole('button', { name: 'Edit', exact: true }).click();
	await dialog.getByLabel('Status', { exact: true }).selectOption('completed');
	await dialog.getByRole('button', { name: 'Save project', exact: true }).click();
	await expect(dialog).toBeHidden();
	await expect(page.getByRole('button', { name: 'Log Time', exact: true })).toHaveCount(0);
	expect(await getProject()).toMatchObject({ status: 'completed', active: false });
	await page.goto(`${base}/finance/projects`);
	await page.getByLabel('Search projects').fill(suffix);
	await expect(page.getByRole('link', { name: `Renamed ${suffix}`, exact: true })).toHaveCount(0);
	await page.getByLabel('Status filter').selectOption('completed');
	await page.getByRole('link', { name: `Renamed ${suffix}`, exact: true }).click();

	await page.getByRole('button', { name: 'Edit', exact: true }).click();
	await dialog.getByLabel('Status', { exact: true }).selectOption('in_progress');
	await dialog.getByRole('button', { name: 'Save project', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Log Time', exact: true })).toBeVisible();
	expect(await getProject()).toMatchObject({ status: 'in_progress', active: true, hourly_rate: 17550, gst_code: 'GST' });
});

test('existing lease onboarding persists co-tenants and opening balances', async ({ page }) => {
	const suffix = crypto.randomUUID();
	const primary = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'tenant', name: `Primary ${suffix}` });
	const co = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'tenant', name: `Co ${suffix}` });
	const property = await api<{ id: number }>(page.request, 'POST', '/properties', { name: `Onboard ${suffix}`, residential: true });
	await page.goto(`${base}/finance/leases/new`);
	await page.getByLabel('Property', { exact: true }).selectOption(String(property.id));
	await page.getByLabel('Tenant', { exact: true }).selectOption(String(primary.id));
	await page.getByLabel(`Co-tenant: Co ${suffix}`, { exact: true }).check();
	await page.getByLabel('Start', { exact: true }).fill('2026-11-01');
	await page.getByLabel('Rent ($)', { exact: true }).fill('500');
	await page.getByLabel('Paid-to date', { exact: true }).fill('2026-11-07');
	await page.getByLabel('Opening arrears ($)', { exact: true }).fill('123.45');
	let release!: () => void;
	let arrived!: () => void;
	const pending = new Promise<void>((resolve) => { release = resolve; });
	const started = new Promise<void>((resolve) => { arrived = resolve; });
	await page.route('**/api/v1/orgs/finance/leases', async (route) => {
		arrived();
		await pending;
		await route.continue();
	});
	const response = page.waitForResponse((r) => r.url().endsWith('/leases') && r.request().method() === 'POST');
	await page.getByRole('button', { name: 'Create lease', exact: true }).click();
	await started;
	try {
		for (const label of ['Property', 'Tenant', 'Start', 'Frequency', 'Rent ($)', 'Bond ($)', 'Paid-to date', 'Opening arrears ($)']) {
			await expect(page.getByLabel(label, { exact: true })).toBeDisabled();
		}
	} finally {
		release();
	}
	const created = await response;
	expect(created.status(), await created.text()).toBe(201);
	const lease = await created.json() as { id: number; opening_arrears_invoice_id: number };
	expect(await api(page.request, 'GET', `/leases/${lease.id}`)).toMatchObject({
		paid_to_date: '2026-11-07', opening_arrears: 12345,
		tenants: expect.arrayContaining([{ contact_id: primary.id, primary: true }, { contact_id: co.id, primary: false }])
	});
	expect(await api(page.request, 'GET', `/invoices/${lease.opening_arrears_invoice_id}`)).toMatchObject({ status: 'draft', total: 12345 });
	await page.goto(`${base}/finance/leases/${lease.id}`);
	await expect(page.getByRole('heading', { name: 'Ledger', exact: true })).toBeVisible();
});

test('billing-run error links retained invoice without creating a second run', async ({ page }) => {
	const suffix = crypto.randomUUID();
	const client = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'client', name: `Recovery ${suffix}` });
	const project = await api<{ id: number }>(page.request, 'POST', '/projects', { name: `Recovery ${suffix}`, client_contact_id: client.id, hourly_rate: 12000 });
	const task = await api<{ id: number }>(page.request, 'POST', `/projects/${project.id}/tasks`, { name: 'Recovery task' });
	await api(page.request, 'POST', '/time-entries', { task_id: task.id, work_date: today, minutes: 45, note: 'Retained run' });
	const before = await api<{ total: number }>(page.request, 'GET', '/invoices');
	let retainedID = 0;
	await page.route('**/api/v1/orgs/finance/billing-runs', async (route) => {
		const body = route.request().postDataJSON();
		expect(body.issue).toBe(true);
		// Persist a real billing run, then simulate the subsequent issue failure.
		const response = await route.fetch({ postData: { ...body, issue: false } });
		expect(response.status()).toBe(201);
		const result = await response.json();
		retainedID = result.invoice.id;
		await route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: 'Simulated issue failure', invoice_id: retainedID }) });
	});
	await page.goto(`${base}/finance/billing-run`);
	await page.getByLabel('Client', { exact: true }).selectOption(String(client.id));
	await page.getByLabel('From', { exact: true }).fill(today);
	await page.getByLabel('To', { exact: true }).fill(today);
	await page.getByLabel('Issue immediately', { exact: true }).check();
	await page.getByRole('button', { name: 'Create invoice', exact: true }).click();
	await expect(page.getByRole('alert')).toHaveText('Simulated issue failure');
	const link = page.getByRole('link', { name: 'Open existing invoice', exact: true });
	await expect(link).toHaveAttribute('href', `/finance/invoices/${retainedID}`);
	await link.click();
	await expect(page).toHaveURL(`${base}/finance/invoices/${retainedID}`);
	expect(await api(page.request, 'GET', `/invoices/${retainedID}`)).toMatchObject({ status: 'draft', total: 9000 });
	expect((await api<{ total: number }>(page.request, 'GET', '/invoices')).total).toBe(before.total + 1);
	const entries = await api<{ entries: { invoice_line_id?: number }[] }>(page.request, 'GET', `/time-entries?project_id=${project.id}`);
	expect(entries.entries[0].invoice_line_id).toBeGreaterThan(0);
});

test('billed-period lease changes save with invoice links and preserve financial records', async ({ page }) => {
	const suffix = crypto.randomUUID();
	const tenant = await api<{ id: number }>(page.request, 'POST', '/contacts', { kind: 'tenant', name: `Warning tenant ${suffix}` });
	const property = await api<{ id: number }>(page.request, 'POST', '/properties', { name: `Warning property ${suffix}`, residential: true });
	const start = new Date(`${today}T00:00:00Z`);
	start.setUTCDate(start.getUTCDate() - 7);
	const startDate = start.toISOString().slice(0, 10);
	const lease = await api<{ id: number }>(page.request, 'POST', '/leases', {
		property_id: property.id, tenants: [{ contact_id: tenant.id, primary: true }], start_date: startDate, frequency: 'weekly', rent_amount: 50000
	});
	await api(page.request, 'POST', '/rent-run');
	const ledger = await api<{ periods: { start: string; end: string; invoice_id: number }[] }>(page.request, 'GET', `/leases/${lease.id}/ledger`);
	expect(ledger.periods.length).toBeGreaterThanOrEqual(2);
	const before = await Promise.all(ledger.periods.map((p) => api(page.request, 'GET', `/invoices/${p.invoice_id}`)));
	await page.goto(`${base}/finance/leases/${lease.id}`);
	const future = new Date(`${ledger.periods.at(-1)!.end}T00:00:00Z`);
	future.setUTCDate(future.getUTCDate() + 1);
	await page.getByRole('button', { name: 'Change rent', exact: true }).click();
	await page.getByLabel('New rent from', { exact: true }).fill(future.toISOString().slice(0, 10));
	await page.getByLabel('Rent ($)', { exact: true }).fill('550');
	await page.getByRole('dialog').getByRole('button', { name: 'Change rent', exact: true }).click();
	await expect(page.getByText('Rent change saved.', { exact: true })).toBeVisible();
	await expect(page.getByRole('status', { name: 'Invoice review required' })).toHaveCount(0);
	start.setUTCDate(start.getUTCDate() + 1);
	await page.getByRole('button', { name: 'Change rent', exact: true }).click();
	await page.getByLabel('New rent from', { exact: true }).fill(start.toISOString().slice(0, 10));
	await page.getByLabel('Rent ($)', { exact: true }).fill('520');
	await page.getByRole('dialog').getByRole('button', { name: 'Change rent', exact: true }).click();
	const panel = page.getByRole('status', { name: 'Invoice review required' });
	await expect(panel).toBeVisible();
	for (const period of ledger.periods) {
		await expect(panel.getByRole('link').filter({ hasText: `Draft invoice ${period.invoice_id}` })).toHaveAttribute('href', `/finance/invoices/${period.invoice_id}`);
	}
	await page.getByRole('button', { name: 'Add charge', exact: true }).click();
	await page.getByLabel('Charge', { exact: true }).fill('Backdated water charge');
	await page.getByLabel('Amount per period ($)', { exact: true }).fill('10');
	await page.getByLabel('From', { exact: true }).fill(startDate);
	await page.getByRole('dialog').getByRole('button', { name: 'Add charge', exact: true }).click();
	await expect(page.getByText(/Charge added\./)).toBeVisible();
	await expect(panel.getByRole('link')).toHaveCount(ledger.periods.length);
	await page.getByRole('button', { name: 'Terminate lease', exact: true }).click();
	await page.getByLabel('Terminate on', { exact: true }).fill(start.toISOString().slice(0, 10));
	page.once('dialog', (dialog) => dialog.accept());
	await page.getByRole('dialog').getByRole('button', { name: 'Terminate lease', exact: true }).click();
	await expect(page.getByText(/Lease terminated\./)).toBeVisible();
	await expect(panel.getByRole('link')).toHaveCount(ledger.periods.length);
	const saved = await api<{ rent_changes: unknown[]; charges: unknown[]; termination_date: string }>(page.request, 'GET', `/leases/${lease.id}`);
	expect(saved.rent_changes).toHaveLength(3);
	expect(saved.charges).toHaveLength(1);
	expect(saved.termination_date).toBe(start.toISOString().slice(0, 10));
	const after = await Promise.all(ledger.periods.map((p) => api(page.request, 'GET', `/invoices/${p.invoice_id}`)));
	expect(after).toEqual(before);
});
