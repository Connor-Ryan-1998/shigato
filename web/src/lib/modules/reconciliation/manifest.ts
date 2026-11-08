import type { ModuleManifest } from '../registry';

export const reconciliation: ModuleManifest = {
	name: 'reconciliation',
	label: 'Reconciliation',
	nav: [
		{ href: 'reconciliation', label: 'Reconciliation', perm: 'reports_read' },
		{ href: 'expenses', label: 'Expenses', perm: 'reports_read' }
	],
	dashboardCard: () => import('./DashboardCard.svelte')
};
