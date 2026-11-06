import type { Component } from 'svelte';

export type ModuleName = 'core' | 'property' | 'timesheets' | 'reconciliation';

export type NavItem = {
	// Relative to /[org].
	href: string;
	label: string;
	// A perm from GET /api/v1/me's perms list, or '' if every signed-in
	// staff member may see this item regardless of perms.
	perm: string;
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

// MODULE_ORDER fixes the sidebar's group order: core first (it's not a
// togglable SHIGOTO_MODULES entry, so it's never in enabledModules), then
// the three optional modules in a stable, product-chosen order.
const MODULE_ORDER: ModuleName[] = ['core', 'property', 'timesheets', 'reconciliation'];

export type VisibleNavGroup = { module: ModuleManifest; items: NavItem[] };

// visibleNav reduces every module manifest to what one user, in one org,
// should actually see: core always shows, the others only when enabled,
// and within a shown module only the nav items whose perm that user holds
// (an item with perm '' is never gated).
export function visibleNav(
	manifests: ModuleManifest[],
	enabledModules: string[],
	perms: string[]
): VisibleNavGroup[] {
	const byName = new Map(manifests.map((m) => [m.name, m]));
	const groups: VisibleNavGroup[] = [];

	for (const name of MODULE_ORDER) {
		const manifest = byName.get(name);
		if (!manifest) continue;
		if (name !== 'core' && !enabledModules.includes(name)) continue;

		const items = manifest.nav.filter((item) => item.perm === '' || perms.includes(item.perm));
		groups.push({ module: manifest, items });
	}

	return groups;
}
