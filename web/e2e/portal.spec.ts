import { test, expect } from '@playwright/test';
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';

// The portal end-to-end: an owner invites a tenant contact to the portal,
// the tenant signs in via a magic link fetched from the compose mailpit
// instance (not a real inbox), and sees their own invoice. Shares the same
// shape as smoke.spec.ts (compose Postgres, bin/shigoto, Chromium) plus
// mailpit for SMTP and its HTTP API.

const root = resolve(import.meta.dirname, '../..');
const binary = join(root, 'bin/shigoto');
const dbName = 'shigoto_e2e_portal';
const databaseURL = `postgres://shigoto:shigoto@localhost:5432/${dbName}?sslmode=disable`;
const ownerEmail = 'owner-portal@example.com';
const password = 'e2e-password';
const mailpitBase = 'http://127.0.0.1:8025';

// Unique per run: the magic-request rate limit is 5/email/hour
// (global-constraints.md), so a rerun against the same mailpit/db must never
// reuse an email from a previous run.
const runID = randomUUID().slice(0, 8);
const tenantEmail = `tenant-${runID}@example.com`;
const otherTenantEmail = `other-tenant-${runID}@example.com`;

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
		SHIGOTO_SCHEDULER: 'off',
		SHIGOTO_SMTP_HOST: '127.0.0.1',
		SHIGOTO_SMTP_PORT: '1025',
		SHIGOTO_SMTP_FROM: 'noreply@shigoto.local',
		SHIGOTO_SMTP_TLS: 'none'
	};
}

function psql(sql: string, database = 'shigoto') {
	execFileSync(
		'docker',
		['compose', 'exec', '-T', 'db', 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'shigoto', '-d', database, '-c', sql],
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
	if (res.status === 204) return undefined as T;
	return (await res.json()) as T;
}

type MailpitMessage = { ID: string; To: { Address: string }[] };
type MailpitListResponse = { messages: MailpitMessage[] };

// waitForMagicLink polls mailpit (magic mail is sent from a detached
// goroutine - Ruling 61 - so it never lands instantly) for the newest
// message to `to`, and extracts the /auth/magic?token=... link from its
// plain-text body.
async function waitForMagicLink(to: string): Promise<string> {
	const deadline = Date.now() + 15_000;
	while (Date.now() < deadline) {
		const list = (await (await fetch(`${mailpitBase}/api/v1/messages`)).json()) as MailpitListResponse;
		const msg = list.messages.find((m) => m.To.some((t) => t.Address === to));
		if (msg) {
			const full = (await (await fetch(`${mailpitBase}/api/v1/message/${msg.ID}`)).json()) as {
				Text: string;
			};
			const match = full.Text.match(/(https?:\/\/\S*\/auth\/magic\?token=\S+)/);
			if (match) return match[1];
		}
		await new Promise((r) => setTimeout(r, 300));
	}
	throw new Error(`no magic-link email arrived for ${to} within the deadline`);
}

test.beforeAll(async () => {
	execFileSync('docker', ['compose', 'up', '-d', '--wait', 'db', 'mailpit'], {
		cwd: root,
		stdio: 'inherit'
	});
	psql(`DROP DATABASE IF EXISTS ${dbName} WITH (FORCE)`);
	psql(`CREATE DATABASE ${dbName}`);
	// mailpit has no per-recipient delete; deleting everything up front
	// keeps a rerun's message search from ever matching a stale message
	// left over by a previous run against the same compose mailpit.
	await fetch(`${mailpitBase}/api/v1/messages`, { method: 'DELETE' });

	base = `http://127.0.0.1:${await freePort()}`;
	filesDir = mkdtempSync(join(tmpdir(), 'shigoto-e2e-portal-'));

	execFileSync(
		binary,
		['admin', 'create-owner', '--org-slug', 'org-a', '--org-name', 'Alpha Pty Ltd', '--email', ownerEmail],
		{ env: env(), input: `${password}\n` }
	);
	execFileSync(
		binary,
		['admin', 'create-owner', '--org-slug', 'org-b', '--org-name', 'Beta Pty Ltd', '--email', ownerEmail],
		{ env: env(), input: `${password}\n` }
	);

	server = spawn(binary, ['serve'], { env: env(), stdio: 'inherit' });
	await waitForHealthy();
});

test.afterAll(() => {
	server?.kill();
	if (filesDir) rmSync(filesDir, { recursive: true, force: true });
});

// login logs the owner in via the JSON API (not the browser) and returns the
// session cookie, for seeding data ahead of the browser-driven steps.
async function ownerLogin(): Promise<string> {
	const res = await fetch(`${base}/api/v1/auth/login`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1' },
		body: JSON.stringify({ email: ownerEmail, password })
	});
	if (!res.ok) throw new Error(`login: ${res.status} ${await res.text()}`);
	return res.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ');
}

// seedTenantInvoice creates a tenant contact with the given email in org,
// issues one invoice to it, and returns the invoice id and its reference.
async function seedTenantInvoice(
	cookie: string,
	org: string,
	email: string
): Promise<{ invoiceID: number; reference: string }> {
	const contact = await api<{ id: number }>(cookie, 'POST', `/api/v1/orgs/${org}/contacts`, {
		kind: 'tenant',
		name: `Tenant ${email}`,
		email
	});
	const inv = await api<{ id: number }>(cookie, 'POST', `/api/v1/orgs/${org}/invoices`, {
		contact_id: contact.id,
		source: 'manual',
		due_in_days: 14,
		lines: [{ description: 'Rent', quantity_milli: 1000, unit_amount: 50000, gst_code: 'N-T' }]
	});
	await api(cookie, 'POST', `/api/v1/orgs/${org}/invoices/${inv.id}/issue`);
	const detail = await api<{ payment_reference: string }>(
		cookie,
		'GET',
		`/api/v1/orgs/${org}/invoices/${inv.id}`
	);
	return { invoiceID: inv.id, reference: detail.payment_reference };
}

test('tenant signs in by magic link and sees only their own invoice', async ({ page }) => {
	const cookie = await ownerLogin();

	const { invoiceID, reference } = await seedTenantInvoice(cookie, 'org-a', tenantEmail);
	// A second tenant, in a different org, whose invoice must stay
	// inaccessible to the first tenant even by guessing its id.
	const other = await seedTenantInvoice(cookie, 'org-b', otherTenantEmail);

	// Invite the first tenant to the portal via the owner's own contact
	// list, same as the Members page's "Invite to portal" action does.
	const contacts = await api<{ id: number; kind: string; email?: string }[]>(
		cookie,
		'GET',
		'/api/v1/orgs/org-a/contacts'
	);
	const tenantContact = contacts.find((c) => c.kind === 'tenant' && c.email === tenantEmail);
	if (!tenantContact) throw new Error('seeded tenant contact not found');
	await api(cookie, 'POST', `/api/v1/orgs/org-a/contacts/${tenantContact.id}/portal-invite`);

	const magicLink = await waitForMagicLink(tenantEmail);

	await page.goto(magicLink);
	await expect(page).toHaveURL(`${base}/portal/org-a/invoices`);

	// The issued invoice, with its payment reference, is visible.
	await expect(page.getByText(reference)).toBeVisible();
	await page.getByRole('link', { name: /^#\d+$/ }).click();
	await expect(page).toHaveURL(`${base}/portal/org-a/invoices/${invoiceID}`);
	await expect(page.getByText(reference, { exact: true })).toBeVisible();

	// The second tenant's invoice id, typed directly into this tenant's own
	// org path, gives "not found" (global-constraints.md "Portal
	// isolation") - a cross-org id, regardless of who it actually belongs
	// to.
	await page.goto(`${base}/portal/org-a/invoices/${other.invoiceID}`);
	await expect(page.getByText('not found')).toBeVisible();

	// Reusing the already-consumed magic link shows the fixed expired
	// message (single-use tokens, global-constraints.md "Tokens").
	await page.goto(magicLink);
	await expect(page.getByText('This link has expired — request a new one')).toBeVisible();

	const session = (await page.context().cookies()).find((c) => c.name === 'shigoto_session');
	expect(session).toBeDefined();
	const hash = createHash('sha256').update(session!.value).digest('hex');
	psql(`UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=decode('${hash}','hex')`, dbName);
	await page.goto(`${base}/portal/org-a/invoices`);
	await expect(page).toHaveURL(new RegExp(`${base}/login(?:\\?|$)`));
	await expect(page.getByRole('dialog', { name: 'Session expired' })).not.toBeVisible();
});
