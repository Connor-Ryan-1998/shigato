import type { ModuleManifest } from '../registry';

// core is always shown: it isn't one of the togglable SHIGOTO_MODULES
// entries, so it's never gated by GET /api/v1/meta's enabled list.
export const core: ModuleManifest = {
	name: 'core',
	label: 'Core',
	nav: [
		{ href: '', label: 'Dashboard', perm: '', group: 'overview' },
		{ href: 'contacts', label: 'Contacts', perm: 'invoices_read', group: 'overview' },
		{ href: 'invoices', label: 'Invoices', perm: 'invoices_read', group: 'finance' },
		{ href: 'reports', label: 'Reports', perm: 'reports_read', group: 'finance' },
		{ href: 'settings', label: 'Settings', perm: 'org_admin', group: 'admin' },
		{ href: 'settings/members', label: 'Members', perm: 'org_admin', group: 'admin' }
	]
};
