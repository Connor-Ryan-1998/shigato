import { test, expect } from '@playwright/test';
import { signIn, startStack, type Stack } from './stack';

// One end-to-end pass over the real stack: two orgs share one owner so the
// org switcher can be exercised.

const email = 'owner@example.com';
const password = 'e2e-password';
let stack: Stack;
let base = '';

// seedOpenInvoices issues one manual, GST-free invoice per amount (in
// cents) to a fresh contact in org, so its dashboard shows them as open.
async function seedOpenInvoices(cookie: string, org: string, amounts: number[]) {
	const contact = await stack.api<{ id: number }>(cookie, 'POST', `/api/v1/orgs/${org}/contacts`, {
		kind: 'client',
		name: `${org} client`
	});
	for (const amount of amounts) {
		const inv = await stack.api<{ id: number }>(cookie, 'POST', `/api/v1/orgs/${org}/invoices`, {
			contact_id: contact.id,
			source: 'manual',
			due_in_days: 14,
			lines: [{ description: 'Work', quantity_milli: 1000, unit_amount: amount, gst_code: 'N-T' }]
		});
		await stack.api(cookie, 'POST', `/api/v1/orgs/${org}/invoices/${inv.id}/issue`);
	}
}

test.beforeAll(async () => {
	stack = await startStack({
		db: 'shigoto_e2e',
		owner: { email, password },
		orgs: [{ slug: 'org-a', name: 'Alpha Pty Ltd' }, { slug: 'org-b', name: 'Beta Pty Ltd' }]
	});
	base = stack.base;
	const cookie = await stack.login(email, password);
	await seedOpenInvoices(cookie, 'org-a', [10000]); // 1 open, $100.00
	await seedOpenInvoices(cookie, 'org-b', [25000, 5000]); // 2 open, $300.00
});

test.afterAll(() => stack?.stop());

test('login, dashboard tiles, and org switching', async ({ page }) => {
	// Orgs are listed by name, so Alpha is the post-login default.
	await signIn(page, base, email, password, '/org-a');
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
