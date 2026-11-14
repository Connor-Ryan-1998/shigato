import { test, expect } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import { startStack, type Stack } from './stack';

// Item 1 (critical, final review): an owner invites a brand-new staff email,
// and a completely anonymous browser (no session cookie at all) opens the
// invite link, creates the account, and lands in the org. Before the fix,
// the root layout's own GET /me probe 401'd for this anonymous visitor and
// bounced the browser straight to /login before the invite page's accept
// form ever rendered.

const ownerEmail = 'owner-invite@example.com';
const password = 'e2e-password';

// Unique per run: a rerun against the same db must never reuse an email a
// previous run already turned into an account.
const runID = randomUUID().slice(0, 8);
const newStaffEmail = `new-staff-${runID}@example.com`;
const newStaffPassword = 'a-very-long-e2e-password';

let stack: Stack;

test.beforeAll(async () => {
	stack = await startStack({
		db: 'shigoto_e2e_invite',
		owner: { email: ownerEmail, password },
		orgs: [{ slug: 'org-a', name: 'Alpha Pty Ltd' }]
	});
});

test.afterAll(() => stack?.stop());

test('a brand-new, anonymous browser accepts a staff invite and lands in the org', async ({
	browser
}) => {
	const cookie = await stack.login(ownerEmail, password);

	// No mailer is configured for this harness (SHIGOTO_SMTP_HOST unset),
	// so the invite response carries the link directly for the owner to
	// hand over by hand (Ruling 53) - exactly what a real owner without
	// SMTP configured would copy and send.
	const created = await stack.api<{ link: string; emailed: boolean }>(
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

	await expect(page).toHaveURL(`${stack.base}/org-a`);

	await context.close();
});
