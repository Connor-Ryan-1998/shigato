import { describe, expect, it } from 'vitest';
import { visibleNav } from './registry';
import { core } from './core/manifest';
import { property } from './property/manifest';
import { reconciliation } from './reconciliation/manifest';
import { timesheets } from './timesheets/manifest';

const manifests = [core, property, timesheets, reconciliation];
const ALL_MODULES = ['property', 'timesheets', 'reconciliation'];
const OWNER_PERMS = [
	'org_admin', 'contacts_write', 'invoices_write', 'invoices_read', 'payments_write', 'reports_read',
	'recon_write', 'property_write', 'property_read', 'timesheets_write', 'timesheets_read', 'timesheets_admin'
];

const hrefs = (groups: ReturnType<typeof visibleNav>, id: string) =>
	groups.find((g) => g.id === id)?.items.map((i) => i.href);

describe('visibleNav', () => {
	it('groups an owner with every module into Overview, Property, Projects, Finance, Admin', () => {
		const groups = visibleNav(manifests, ALL_MODULES, OWNER_PERMS);
		expect(groups.map((g) => g.label)).toEqual(['Overview', 'Property', 'Projects', 'Finance', 'Admin']);
		expect(hrefs(groups, 'overview')).toEqual(['', 'contacts']);
		expect(hrefs(groups, 'projects')).toEqual(['projects', 'time']);
		expect(hrefs(groups, 'finance')).toEqual(['invoices', 'reports', 'reconciliation', 'expenses']);
		expect(hrefs(groups, 'admin')).toEqual(['settings', 'settings/members']);
	});

	it('drops reconciliation links from Finance when that module is off', () => {
		const groups = visibleNav(manifests, ['property', 'timesheets'], OWNER_PERMS);
		expect(hrefs(groups, 'finance')).toEqual(['invoices', 'reports']);
	});

	it('shows only Overview with the dashboard to a user with no perms', () => {
		const groups = visibleNav(manifests, ALL_MODULES, []);
		expect(groups.map((g) => g.id)).toEqual(['overview']);
		expect(hrefs(groups, 'overview')).toEqual(['']);
	});

	it('omits Projects when timesheets is disabled', () => {
		const groups = visibleNav(manifests, ['property', 'reconciliation'], OWNER_PERMS);
		expect(groups.some((g) => g.id === 'projects')).toBe(false);
	});

	it('has no standalone billing-run link', () => {
		const groups = visibleNav(manifests, ALL_MODULES, OWNER_PERMS);
		expect(groups.flatMap((g) => g.items).some((i) => i.href === 'billing-run')).toBe(false);
	});

	it.each([
		{ perms: ['timesheets_read'], projects: ['projects', 'time'] },
		{ perms: ['invoices_write'], projects: undefined }
	])('exposes timesheet screens according to permissions: $perms', ({ perms, projects }) => {
		expect(hrefs(visibleNav(manifests, ['timesheets'], perms), 'projects')).toEqual(projects);
	});

	it('shows Expenses only with enabled reconciliation and reports permission', () => {
		expect(hrefs(visibleNav(manifests, ['reconciliation'], ['reports_read']), 'finance')).toContain('expenses');
		expect(hrefs(visibleNav(manifests, [], ['reports_read']), 'finance')).not.toContain('expenses');
		expect(hrefs(visibleNav(manifests, ['reconciliation'], ['invoices_read']), 'finance')).not.toContain('expenses');
	});

	it('shows the Contacts link to readers but not portal users', () => {
		expect(hrefs(visibleNav(manifests, [], ['invoices_read']), 'overview')).toContain('contacts');
		expect(hrefs(visibleNav(manifests, [], []), 'overview')).not.toContain('contacts');
	});

	it('shows Settings and Members only to org admins', () => {
		expect(hrefs(visibleNav(manifests, [], ['org_admin']), 'admin')).toEqual(['settings', 'settings/members']);
		expect(hrefs(visibleNav(manifests, [], ['invoices_read']), 'admin')).toBeUndefined();
	});

	it('hides a disabled module and a perm-less item', () => {
		expect(hrefs(visibleNav(manifests, [], ['property_read']), 'property')).toBeUndefined();
		expect(hrefs(visibleNav(manifests, ['property'], []), 'property')).toBeUndefined();
	});
});
