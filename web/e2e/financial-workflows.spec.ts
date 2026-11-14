import { test, expect, type APIRequestContext } from '@playwright/test';
import { createHash } from 'node:crypto';
import { signIn, startStack, type Stack } from './stack';

// Connection paths through the real stack: each test proves the pieces join
// up (SPA, API, database), and the browser-only behaviour no unit test can
// see (focus, layout, navigation guards, session expiry). Rules owned by Go
// or Vitest tests are not re-checked here (docs/testing.md).

const email = 'finance-owner@example.com';
const password = 'financial-e2e-password';
const today = new Date().toISOString().slice(0, 10);
const apiBase = '/api/v1/orgs/finance';
let stack: Stack;
let base = '';

test.use({ timezoneId: 'UTC' });

// api calls the JSON API with the signed-in page's cookies.
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
	stack = await startStack({
		db: 'shigoto_e2e_financial',
		owner: { email, password },
		orgs: [{ slug: 'finance', name: 'Finance tests' }],
		env: {
			SHIGOTO_TIMEZONE: 'UTC', SHIGOTO_MODULES: 'property,timesheets,reconciliation', SHIGOTO_SMTP_HOST: '',
			SHIGOTO_OIDC_ISSUER: '', SHIGOTO_OIDC_CLIENT_ID: '', SHIGOTO_OIDC_CLIENT_SECRET: '',
			SHIGOTO_STRIPE_FINANCE_SECRET_KEY: '', SHIGOTO_STRIPE_FINANCE_WEBHOOK_SECRET: ''
		},
		seed: (psql) => psql(`INSERT INTO contacts (org_id,kind,name) SELECT id,'client','Finance client' FROM orgs;
			INSERT INTO contacts (org_id,kind,name) SELECT id,'tenant','Finance tenant' FROM orgs;`)
	});
	base = stack.base;
});

test.afterAll(() => stack?.stop());

test.beforeEach(({ page }) => signIn(page, base, email, password, '/finance'));

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
	stack.psql(`UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=decode('${hash}','hex')`);
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
	await page.goto(`${base}/finance/invoices`);
	await page.getByRole('button', { name: 'Billing run', exact: true }).click();
	const run = page.getByRole('dialog', { name: 'Billing run' });
	await run.getByLabel('Client', { exact: true }).selectOption(String(client.id));
	await run.getByLabel('From', { exact: true }).fill(today);
	await run.getByLabel('To', { exact: true }).fill(today);
	await expect(run.getByRole('checkbox', { name: /Consulting/ })).toBeChecked();
	await run.getByRole('button', { name: 'Create invoices', exact: true }).click();
	await expect(page).toHaveURL(new RegExp(`${base}/finance/invoices/\\d+$`));
	const inv = await api<Invoice>(page.request, 'GET', `/invoices/${page.url().split('/').at(-1)}`);
	expect(inv).toMatchObject({ status: 'draft', total: 10000 });
	const beforeRetry = await api<{ total: number }>(page.request, 'GET', '/invoices');
	// Replaying the reviewed selection after it was billed is refused, not re-billed.
	const tasks = await api<{ id: number }[]>(page.request, 'GET', `/projects/${project.id}/tasks`);
	const duplicate = await page.request.post(`${base}${apiBase}/billing-runs`, { headers: { 'X-Shigoto-CSRF': '1' }, data: { client_contact_id: client.id, from: today, to: today, due_in_days: 14, split: 'combined', tasks: [{ task_id: tasks[0].id, minutes: 50, amount: 10000 }] } });
	expect(duplicate.status()).toBe(409);
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
	await expect(page.getByRole('link', { name: 'Edit Finance cottage', exact: true })).toBeVisible();
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

test('property details persist and reach the Item 21 report', async ({ page }) => {
	await page.goto(`${base}/finance/properties`);
	await page.getByRole('button', { name: 'New property', exact: true }).click();
	await page.getByLabel('Name', { exact: true }).fill('Details cottage');
	await page.getByRole('button', { name: 'Create property', exact: true }).click();
	await page.getByRole('link', { name: 'Edit Details cottage', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Details cottage', exact: true })).toBeVisible();
	await page.getByLabel('Purchase price ($)', { exact: true }).fill('650000');
	await page.getByRole('button', { name: 'Save details', exact: true }).click();
	await expect(page.getByText('Details saved.')).toBeVisible();
	await page.reload();
	await expect(page.getByLabel('Purchase price ($)', { exact: true })).toHaveValue('650000.00');
	await page.getByRole('link', { name: 'Notes', exact: true }).click();
	await page.getByLabel('Notes', { exact: true }).fill('Gate code 1234');
	await page.getByRole('button', { name: 'Save notes', exact: true }).click();
	await expect(page.getByText('Notes saved.')).toBeVisible();
	await page.getByRole('link', { name: 'Depreciation', exact: true }).click();
	await page.getByRole('link', { name: 'Item 21 report', exact: true }).click();
	await expect(page.getByRole('heading', { name: /^Item 21 rental schedule FY/ })).toBeVisible();
	await expect(page.getByRole('table', { name: 'Details cottage' })).toContainText('Net rent');
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

