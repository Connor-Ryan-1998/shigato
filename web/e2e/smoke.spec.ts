import { test, expect } from '@playwright/test';
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

// One end-to-end pass over the real stack: compose Postgres, the built
// bin/shigoto (with the SPA embedded), and Chromium. Two orgs share one
// owner so the org switcher can be exercised.

const root = resolve(import.meta.dirname, '../..');
const binary = join(root, 'bin/shigoto');
const dbName = 'shigoto_e2e';
const databaseURL = `postgres://shigoto:shigoto@localhost:5432/${dbName}?sslmode=disable`;
const email = 'owner@example.com';
const password = 'e2e-password';

let base = '';
let server: ChildProcess | undefined;
let filesDir = '';

function freePort(): Promise<number> {
	return new Promise((ok, fail) => {
		const srv = createServer();
		srv.on('error', fail);
		srv.listen(0, '127.0.0.1', () => {
			const addr = srv.address();
			const port = typeof addr === 'object' && addr ? addr.port : 0;
			srv.close(() => ok(port));
		});
	});
}

function env(): NodeJS.ProcessEnv {
	return {
		...process.env,
		SHIGOTO_DATABASE_URL: databaseURL,
		SHIGOTO_BASE_URL: base,
		SHIGOTO_LISTEN: base.replace('http://', ''),
		SHIGOTO_FILES_DIR: filesDir,
		SHIGOTO_SCHEDULER: 'off'
	};
}

function psql(sql: string) {
	execFileSync('docker', ['compose', 'exec', '-T', 'db', 'psql', '-q', '-U', 'shigoto', '-d', 'shigoto', '-c', sql], {
		cwd: root,
		stdio: 'pipe'
	});
}

async function waitForHealthy() {
	for (let i = 0; i < 100; i++) {
		try {
			if ((await fetch(`${base}/healthz`)).ok) return;
		} catch {
			// not listening yet
		}
		await new Promise((r) => setTimeout(r, 100));
	}
	throw new Error('shigoto did not become healthy');
}

// api calls the JSON API as the owner, with the CSRF header every unsafe
// request needs.
async function api<T>(cookie: string, method: string, path: string, body?: unknown): Promise<T> {
	const res = await fetch(`${base}${path}`, {
		method,
		headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1', Cookie: cookie },
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
	return (await res.json()) as T;
}

// seedOpenInvoices issues one manual, GST-free invoice per amount (in
// cents) to a fresh contact in org, so its dashboard shows them as open.
async function seedOpenInvoices(cookie: string, org: string, amounts: number[]) {
	const contact = await api<{ id: number }>(cookie, 'POST', `/api/v1/orgs/${org}/contacts`, {
		kind: 'client',
		name: `${org} client`
	});
	for (const amount of amounts) {
		const inv = await api<{ id: number }>(cookie, 'POST', `/api/v1/orgs/${org}/invoices`, {
			contact_id: contact.id,
			source: 'manual',
			due_in_days: 14,
			lines: [{ description: 'Work', quantity_milli: 1000, unit_amount: amount, gst_code: 'N-T' }]
		});
		await api(cookie, 'POST', `/api/v1/orgs/${org}/invoices/${inv.id}/issue`);
	}
}

test.beforeAll(async () => {
	execFileSync('docker', ['compose', 'up', '-d', '--wait', 'db'], { cwd: root, stdio: 'inherit' });
	psql(`DROP DATABASE IF EXISTS ${dbName} WITH (FORCE)`);
	psql(`CREATE DATABASE ${dbName}`);

	base = `http://127.0.0.1:${await freePort()}`;
	filesDir = mkdtempSync(join(tmpdir(), 'shigoto-e2e-'));

	// create-owner migrates the database, and adds a second org to the same
	// owner when the email already exists.
	for (const [slug, name] of [
		['org-a', 'Alpha Pty Ltd'],
		['org-b', 'Beta Pty Ltd']
	]) {
		execFileSync(binary, ['admin', 'create-owner', '--org-slug', slug, '--org-name', name, '--email', email], {
			env: env(),
			input: `${password}\n`
		});
	}

	server = spawn(binary, ['serve'], { env: env(), stdio: 'inherit' });
	await waitForHealthy();

	const login = await fetch(`${base}/api/v1/auth/login`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1' },
		body: JSON.stringify({ email, password })
	});
	if (!login.ok) throw new Error(`login: ${login.status} ${await login.text()}`);
	const cookie = login.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ');

	await seedOpenInvoices(cookie, 'org-a', [10000]); // 1 open, $100.00
	await seedOpenInvoices(cookie, 'org-b', [25000, 5000]); // 2 open, $300.00
});

test.afterAll(() => {
	server?.kill();
	if (filesDir) rmSync(filesDir, { recursive: true, force: true });
});

test('login, dashboard tiles, and org switching', async ({ page }) => {
	await page.goto(`${base}/login`);
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password').fill(password);
	await page.getByRole('button', { name: 'Sign in' }).click();

	// Orgs are listed by name, so Alpha is the post-login default.
	await expect(page).toHaveURL(`${base}/org-a`);
	const openTile = page.getByRole('link', { name: /open invoices/i });
	await expect(openTile).toContainText('$100.00');

	await openTile.click();
	await expect(page).toHaveURL(`${base}/org-a/invoices?status=open`);
	await expect(page.getByRole('link', { name: 'Open', exact: true })).toHaveAttribute('aria-current', 'page');

	// Back on org A's dashboard, switch to org B: the dashboard route stays
	// the same, so this is exactly where stale data used to linger.
	await page.goBack();
	await expect(openTile).toContainText('$100.00');
	await page.getByLabel('Organisation').selectOption('org-b');
	await expect(page).toHaveURL(`${base}/org-b`);
	await expect(openTile).toContainText('$300.00');
	await expect(openTile).toContainText('2');
});
