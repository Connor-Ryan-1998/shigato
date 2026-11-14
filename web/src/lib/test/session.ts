// signIn puts a signed-in user in the shared session store, the way the
// root layout does after GET /api/v1/me.

import { session, type MeOrg } from '../session.svelte';

// OWNER_PERMS mirrors the owner row of internal/auth/rbac.go. Other roles
// pass perms explicitly, so a test states exactly what the UI may show.
export const OWNER_PERMS = [
	'org_admin', 'contacts_write', 'invoices_write', 'invoices_read', 'payments_write', 'reports_read',
	'recon_write', 'property_write', 'property_read', 'timesheets_write', 'timesheets_read', 'timesheets_admin'
];

export function signIn({
	role = 'owner',
	org = 'acme',
	perms = role === 'owner' ? OWNER_PERMS : [],
	orgs = [],
	user = { id: 1, email: 'owner@example.com', name: 'Owner' }
}: {
	role?: string;
	org?: string;
	perms?: string[];
	orgs?: MeOrg[];
	user?: { id: number; email: string; name: string };
} = {}): void {
	session.setMe({ user, orgs: [{ slug: org, name: org, role, perms }, ...orgs] });
	session.setOrg(org);
}
