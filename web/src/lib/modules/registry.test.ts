import { describe, expect, it } from 'vitest';
import type { ModuleManifest } from './registry';
import { visibleNav } from './registry';

const manifests: ModuleManifest[] = [
	{
		name: 'core',
		label: 'Core',
		nav: [
			{ href: '', label: 'Dashboard', perm: '' },
			{ href: 'invoices', label: 'Invoices', perm: 'invoices_read' }
		]
	},
	{
		name: 'property',
		label: 'Property',
		nav: [{ href: 'leases', label: 'Leases', perm: 'property_read' }]
	},
	{
		name: 'timesheets',
		label: 'Timesheets',
		nav: []
	},
	{
		name: 'reconciliation',
		label: 'Reconciliation',
		nav: [{ href: 'reconciliation', label: 'Reconciliation', perm: 'reports_read' }]
	}
];

describe('visibleNav', () => {
	it('hides a disabled module', () => {
		const groups = visibleNav(manifests, [], ['property_read']);
		expect(groups.some((g) => g.module.name === 'property')).toBe(false);
	});

	it('hides an item the user lacks the perm for', () => {
		const groups = visibleNav(manifests, ['property'], []);
		const property = groups.find((g) => g.module.name === 'property');
		expect(property?.items).toHaveLength(0);
	});

	it('always puts core first and includes it even when not "enabled"', () => {
		const groups = visibleNav(manifests, ['property', 'timesheets', 'reconciliation'], []);
		expect(groups[0].module.name).toBe('core');
		// Dashboard has no perm requirement, so it's always visible.
		expect(groups[0].items.map((i) => i.label)).toContain('Dashboard');
	});
});
