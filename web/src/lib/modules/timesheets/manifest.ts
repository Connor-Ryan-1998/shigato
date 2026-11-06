import type { ModuleManifest } from '../registry';

// Nav items arrive in a later plan; this task only wires the module into
// the registry so the sidebar group order (core, property, timesheets,
// reconciliation) is already correct once they do.
export const timesheets: ModuleManifest = {
	name: 'timesheets',
	label: 'Timesheets',
	nav: [],
	dashboardCard: () => import('./DashboardCard.svelte')
};
