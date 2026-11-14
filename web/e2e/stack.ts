// stack starts the real application for one spec file: a fresh database in
// compose Postgres, the built bin/shigoto (SPA embedded) on a free port, and
// owners created through the CLI. Each spec gets its own database so specs
// never see each other's data.

import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { expect, type Page } from '@playwright/test';

const root = resolve(import.meta.dirname, '../..');
const binary = join(root, 'bin/shigoto');

export type Stack = {
	base: string;
	// psql runs SQL against this stack's database (or another, e.g. 'shigoto').
	psql(sql: string, database?: string): string;
	// login signs in through the JSON API and returns the session cookie.
	login(email: string, password: string): Promise<string>;
	// api calls the JSON API with a session cookie and the CSRF header.
	api<T>(cookie: string, method: string, path: string, body?: unknown): Promise<T>;
	stop(): Promise<void>;
};

export type StackOptions = {
	db: string;
	owner: { email: string; password: string };
	orgs: { slug: string; name: string }[];
	services?: ('db' | 'mailpit')[];
	env?: Record<string, string>;
	// seed runs after migrations (create-owner) and before the server starts.
	seed?: (psql: Stack['psql']) => void;
};

export async function startStack(opts: StackOptions): Promise<Stack> {
	execFileSync('docker', ['compose', 'up', '-d', '--wait', ...(opts.services ?? ['db'])], { cwd: root, stdio: 'inherit' });
	const psql = (sql: string, database = opts.db) =>
		execFileSync('docker', ['compose', 'exec', '-T', 'db', 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'shigoto', '-d', database, '-c', sql], {
			cwd: root,
			encoding: 'utf8'
		});
	psql(`DROP DATABASE IF EXISTS ${opts.db} WITH (FORCE)`, 'shigoto');
	psql(`CREATE DATABASE ${opts.db}`, 'shigoto');

	const base = `http://127.0.0.1:${await freePort()}`;
	const filesDir = mkdtempSync(join(tmpdir(), `${opts.db}-`));
	const env = {
		...process.env,
		SHIGOTO_DATABASE_URL: `postgres://shigoto:shigoto@localhost:5432/${opts.db}?sslmode=disable`,
		SHIGOTO_BASE_URL: base,
		SHIGOTO_LISTEN: base.replace('http://', ''),
		SHIGOTO_FILES_DIR: filesDir,
		SHIGOTO_SCHEDULER: 'off',
		...opts.env
	};
	// create-owner migrates the database, and adds each further org to the
	// same owner.
	for (const org of opts.orgs) {
		execFileSync(binary, ['admin', 'create-owner', '--org-slug', org.slug, '--org-name', org.name, '--email', opts.owner.email], {
			env,
			input: `${opts.owner.password}\n`
		});
	}
	opts.seed?.(psql);

	const server: ChildProcess = spawn(binary, ['serve'], { env, stdio: 'inherit' });
	await expect.poll(async () => {
		try {
			return (await fetch(`${base}/healthz`)).ok;
		} catch {
			return false;
		}
	}, { timeout: 10_000 }).toBe(true);

	return {
		base,
		psql,
		async login(email, password) {
			const res = await fetch(`${base}/api/v1/auth/login`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1' },
				body: JSON.stringify({ email, password })
			});
			if (!res.ok) throw new Error(`login: ${res.status} ${await res.text()}`);
			return res.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ');
		},
		async api<T>(cookie: string, method: string, path: string, body?: unknown) {
			const res = await fetch(`${base}${path}`, {
				method,
				headers: { 'Content-Type': 'application/json', 'X-Shigoto-CSRF': '1', Cookie: cookie },
				body: body === undefined ? undefined : JSON.stringify(body)
			});
			if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
			if (res.status === 204) return undefined as T;
			return (await res.json()) as T;
		},
		async stop() {
			if (server.exitCode === null && server.signalCode === null) {
				const exited = once(server, 'exit');
				server.kill();
				await exited;
			}
			rmSync(filesDir, { recursive: true, force: true });
		}
	};
}

// signIn logs a browser page in through the login form and waits for the
// landing URL.
export async function signIn(page: Page, base: string, email: string, password: string, landing: string) {
	await page.goto(`${base}/login`);
	await page.getByLabel('Email', { exact: true }).fill(email);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).toHaveURL(`${base}${landing}`);
}

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
