import type { ModuleManifest } from '../registry';

// core is always shown: it isn't one of the togglable SHIGOTO_MODULES
// entries, so it's never gated by GET /api/v1/meta's enabled list.
export const core: ModuleManifest = {
	name: 'core',
	label: 'Core',
	nav: [
		{ href: '', label: 'Dashboard', perm: '' },
		{ href: 'invoices', label: 'Invoices', perm: 'invoices_read' },
		{ href: 'settings/members', label: 'Members', perm: 'org_admin' }
	]
};
