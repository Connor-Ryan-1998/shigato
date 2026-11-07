import { test, expect } from '@playwright/test';
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { randomUUID } from 'node:crypto';

// Item 1 (critical, final review): an owner invites a brand-new staff email,
// and a completely anonymous browser (no session cookie at all) opens the
// invite link, creates the account, and lands in the org. Before the fix,
// the root layout's own GET /me probe 401'd for this anonymous visitor and
// bounced the browser straight to /login before the invite page's accept
// form ever rendered. Shares the same shape as portal.spec.ts and
// smoke.spec.ts (compose Postgres, bin/shigoto, Chromium).

const root = resolve(import.meta.dirname, '../..');
const binary = join(root, 'bin/shigoto');
const dbName = 'shigoto_e2e_invite';
const databaseURL = `postgres://shigoto:shigoto@localhost:5432/${dbName}?sslmode=disable`;
const ownerEmail = 'owner-invite@example.com';
const password = 'e2e-password';

// Unique per run: a rerun against the same db must never reuse an email a
// previous run already turned into an account.
const runID = randomUUID().slice(0, 8);
const newStaffEmail = `new-staff-${runID}@example.com`;
const newStaffPassword = 'a-very-long-e2e-password';

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
	execFileSync(
		'docker',
		['compose', 'exec', '-T', 'db', 'psql', '-q', '-U', 'shigoto', '-d', 'shigoto', '-c', sql],
		{ cwd: root, stdio: 'pipe' }
	);
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

async function api<T>(cookie: string, method: string, path: string, body?: unknown): Promise<T> {
	const res = await fetch(`${base}${path}`, {
		method,
		headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1', Cookie: cookie },
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
	return (await res.json()) as T;
}

test.beforeAll(async () => {
	execFileSync('docker', ['compose', 'up', '-d', '--wait', 'db'], { cwd: root, stdio: 'inherit' });
	psql(`DROP DATABASE IF EXISTS ${dbName} WITH (FORCE)`);
	psql(`CREATE DATABASE ${dbName}`);

	base = `http://127.0.0.1:${await freePort()}`;
	filesDir = mkdtempSync(join(tmpdir(), 'shigoto-e2e-invite-'));

	execFileSync(
		binary,
		['admin', 'create-owner', '--org-slug', 'org-a', '--org-name', 'Alpha Pty Ltd', '--email', ownerEmail],
		{ env: env(), input: `${password}\n` }
	);

	server = spawn(binary, ['serve'], { env: env(), stdio: 'inherit' });
	await waitForHealthy();
});

test.afterAll(() => {
	server?.kill();
	if (filesDir) rmSync(filesDir, { recursive: true, force: true });
});

async function ownerLogin(): Promise<string> {
	const res = await fetch(`${base}/api/v1/auth/login`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1' },
		body: JSON.stringify({ email: ownerEmail, password })
	});
	if (!res.ok) throw new Error(`login: ${res.status} ${await res.text()}`);
	return res.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ');
}

test('a brand-new, anonymous browser accepts a staff invite and lands in the org', async ({
	browser
}) => {
	const cookie = await ownerLogin();

	// No mailer is configured for this harness (SHIGOTO_SMTP_HOST unset),
	// so the invite response carries the link directly for the owner to
	// hand over by hand (Ruling 53) - exactly what a real owner without
	// SMTP configured would copy and send.
	const created = await api<{ link: string; emailed: boolean }>(
		cookie,
		'POST',
		'/api/v1/orgs/org-a/invites',
		{ email: newStaffEmail, role: 'staff' }
	);
	expect(created.emailed).toBe(false);
	expect(created.link).toContain('/invite/');

	// A fresh, cookie-less browser context: nothing from the owner's
	// session (or any session at all) leaks into this visitor.
	const context = await browser.newContext();
	const page = await context.newPage();

	await page.goto(created.link);
	await expect(page.getByRole('heading', { name: 'Join Alpha Pty Ltd' })).toBeVisible();

	await page.getByLabel('Your name').fill('Brand New Staffer');
	await page.getByLabel('Password').fill(newStaffPassword);
	await page.getByRole('button', { name: /create account and accept/i }).click();

	await expect(page).toHaveURL(`${base}/org-a`);

	await context.close();
});
