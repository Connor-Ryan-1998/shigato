import type { ModuleManifest } from '../registry';

export const reconciliation: ModuleManifest = {
	name: 'reconciliation',
	label: 'Reconciliation',
	nav: [
		{ href: 'reconciliation', label: 'Reconciliation', perm: 'reports_read', group: 'finance' },
		{ href: 'expenses', label: 'Expenses', perm: 'reports_read', group: 'finance' }
	],
	dashboardCard: () => import('./DashboardCard.svelte')
};
