import { afterEach, describe, expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { render, screen } from '@testing-library/svelte';

const page = { params: { org: 'acme' }, url: new URL('http://localhost/portal/acme/invoices') };
vi.mock('$app/state', () => ({
	get page() {
		return page;
	}
}));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

import Layout from './+layout.svelte';
import { session } from '../../lib/session.svelte';

function childSnippet() {
	return createRawSnippet(() => ({ render: () => '<p data-testid="child"></p>' }));
}

function meWithOrgs(orgs: { slug: string; name: string; role: string; portal?: boolean }[]) {
	return {
		user: { id: 1, email: 'tenant@example.com', name: 'Tenant' },
		orgs: orgs.map((o) => ({ ...o, perms: [] }))
	};
}

describe('portal layout', () => {
	it('offers skip navigation and identifies the current portal section without staff links', () => {
		session.setMe(meWithOrgs([{ slug: 'acme', name: 'Acme', role: 'tenant', portal: true }]));
		render(Layout, { children: childSnippet() });
		expect(screen.getByRole('link', { name: 'Skip to content' }).getAttribute('href')).toBe('#main-content');
		expect(screen.getByRole('link', { name: 'Invoices' }).getAttribute('aria-current')).toBe('page');
		expect(screen.queryByRole('navigation', { name: 'Primary' })).toBeNull();
	});
	afterEach(() => {
		session.clear();
	});

	// Item 9: a user who holds both a portal membership and a staff
	// membership (e.g. an owner who's also a tenant of their own property)
	// needs a way back to their staff org without signing out of the portal
	// shell first.
	it('links back to the first staff org when the user also has one', () => {
		session.setMe(
			meWithOrgs([
				{ slug: 'acme', name: 'Acme', role: 'tenant', portal: true },
				{ slug: 'beta', name: 'Beta Co', role: 'owner' }
			])
		);

		render(Layout, { children: childSnippet() });

		const link = screen.getByRole('link', { name: /Beta Co/ });
		expect(link.getAttribute('href')).toBe('/beta');
	});

	it('shows no staff-org link for a portal-only user', () => {
		session.setMe(meWithOrgs([{ slug: 'acme', name: 'Acme', role: 'tenant', portal: true }]));

		render(Layout, { children: childSnippet() });

		expect(screen.queryByRole('link', { name: /Back to/ })).toBeNull();
	});
});
