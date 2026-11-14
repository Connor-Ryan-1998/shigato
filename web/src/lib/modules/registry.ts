import type { Component } from 'svelte';

export type ModuleName = 'core' | 'property' | 'timesheets' | 'reconciliation';

// NavGroup is the sidebar section a link appears in. It is a display
// grouping only: which module owns a link still decides whether it shows.
export type NavGroup = 'overview' | 'property' | 'projects' | 'finance' | 'admin';

// NAV_GROUPS fixes the sidebar's section order and labels.
export const NAV_GROUPS: { id: NavGroup; label: string }[] = [
	{ id: 'overview', label: 'Overview' },
	{ id: 'property', label: 'Property' },
	{ id: 'projects', label: 'Projects' },
	{ id: 'finance', label: 'Finance' },
	{ id: 'admin', label: 'Admin' }
];

export type NavItem = {
	// Relative to /[org].
	href: string;
	label: string;
	// A perm from GET /api/v1/me's perms list, or '' if every signed-in
	// staff member may see this item regardless of perms.
	perm: string;
	group: NavGroup;
};

export type ModuleManifest = {
	name: ModuleName;
	label: string;
	nav: NavItem[];
	// `any` props: each module's dashboard card takes its own `data` prop
	// shaped like that module's dashboard response section, which varies
	// per module - the dashboard page passes it straight through unopened.
	dashboardCard?: () => Promise<{ default: Component<any> }>;
};

// MODULE_ORDER orders links within a sidebar section: core first (it's
// not a togglable SHIGOTO_MODULES entry, so it's never in enabledModules),
// then the optional modules in a stable, product-chosen order.
const MODULE_ORDER: ModuleName[] = ['core', 'property', 'timesheets', 'reconciliation'];

export type VisibleNavGroup = { id: NavGroup; label: string; items: NavItem[] };

// visibleNav reduces every module manifest to what one user, in one org,
// should actually see, grouped into sidebar sections: core always counts,
// the other modules only when enabled, and only the items whose perm that
// user holds (an item with perm '' is never gated). Sections follow
// NAV_GROUPS order and empty ones are left out.
export function visibleNav(
	manifests: ModuleManifest[],
	enabledModules: string[],
	perms: string[]
): VisibleNavGroup[] {
	const byName = new Map(manifests.map((m) => [m.name, m]));
	const items: NavItem[] = [];
	for (const name of MODULE_ORDER) {
		const manifest = byName.get(name);
		if (!manifest) continue;
		if (name !== 'core' && !enabledModules.includes(name)) continue;
		items.push(...manifest.nav.filter((item) => item.perm === '' || perms.includes(item.perm)));
	}
	return NAV_GROUPS.map((g) => ({ ...g, items: items.filter((item) => item.group === g.id) })).filter((g) => g.items.length > 0);
}
