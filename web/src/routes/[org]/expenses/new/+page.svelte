<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../lib/api';
	import { session } from '../../../../lib/session.svelte';
	import { todayLocal } from '../../../../lib/format';
	import { useUnsavedChanges } from '../../../../lib/forms/unsaved.svelte';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import LoadState from '../../../../lib/components/LoadState.svelte';
	import ExpenseForm, { type Category, type SimpleOption, type NewExpense } from '../../../../lib/modules/reconciliation/ExpenseForm.svelte';
	const org = $derived(page.params.org ?? '');
	const member = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const allowed = $derived(session.modules.includes('reconciliation') && !!member?.perms.includes('reports_read') && !!member?.perms.includes('recon_write'));
	const canReadContacts = $derived(!!member?.perms.includes('invoices_read'));
	const propertyEnabled = $derived(session.modules.includes('property'));
	const timesheetsEnabled = $derived(session.modules.includes('timesheets'));
	// "Add bill" from a property passes ?property_id= to seed the first line.
	const initialPropertyId = $derived(page.url?.searchParams.get('property_id') ?? '');
	const optionScope = $derived(`${org}:${canReadContacts}:${propertyEnabled}:${timesheetsEnabled}`);
	let categories = $state<Category[]>([]);
	let suppliers = $state<SimpleOption[]>([]);
	let properties = $state<SimpleOption[] | undefined>();
	let projects = $state<SimpleOption[] | undefined>();
	let loaded = $state('');
	let error = $state('');
	let saveError = $state('');
	let saving = $state(false);
	let uploading = $state(false);
	let dirty = $state(false);
	let version = $state(0);
	let savedID = $state<number | null>(null);
	let warnings = $state<string[]>([]);
	let seq = 0;
	const { allowLeave } = useUnsavedChanges(() => dirty, () => saving || uploading);
	async function load() {
		const token = ++seq;
		const slug = org;
		const scope = optionScope;
		error = '';
		try {
			const base = `/api/v1/orgs/${slug}`;
			const result = await Promise.all([
				api<Category[]>('GET', `${base}/categories`),
				canReadContacts ? api<SimpleOption[]>('GET', `${base}/contacts?kind=supplier`) : Promise.resolve([]),
				propertyEnabled ? api<SimpleOption[]>('GET', `${base}/properties`) : Promise.resolve(undefined),
				timesheetsEnabled ? api<SimpleOption[]>('GET', `${base}/projects`) : Promise.resolve(undefined)
			]);
			if (token !== seq || scope !== optionScope || !allowed) return;
			[categories, suppliers, properties, projects] = result; loaded = scope;
		} catch (e) { if (token === seq && scope === optionScope) error = e instanceof ApiError ? e.message : 'Could not load expense options.'; }
	}
	$effect(() => {
		optionScope; allowed;
		untrack(() => { loaded = ''; categories = []; suppliers = []; properties = undefined; projects = undefined; dirty = false; saving = false; uploading = false; savedID = null; warnings = []; saveError = ''; if (allowed) load(); });
		return () => { seq++; };
	});
	async function save(expense: NewExpense) {
		if (!allowed || loaded !== optionScope || saving || uploading) return;
		const token = seq;
		const slug = org;
		const scope = optionScope;
		saving = true; saveError = '';
		try {
			const result = await api<{ id: number; warnings?: string[] }>('POST', `/api/v1/orgs/${slug}/expenses`, expense);
			if (token !== seq || scope !== optionScope || !allowed) return;
			savedID = result.id; warnings = result.warnings ?? []; dirty = false; version++;
		} catch (e) { if (token === seq) saveError = e instanceof ApiError ? e.message : 'Could not save expense.'; }
		finally { if (token === seq) saving = false; }
	}
	async function uploadReceipt(file: File) {
		const slug = org;
		const scope = optionScope;
		const token = seq;
		if (!allowed || loaded !== optionScope || saving || uploading) throw new Error('Expense form is no longer available.');
		uploading = true;
		try {
			const body = new FormData(); body.append('file', file);
			const result = await api<{ id: number }>('POST', `/api/v1/orgs/${slug}/files`, body);
			if (token !== seq || scope !== optionScope || !allowed) throw new Error('Expense form has changed.');
			return result.id;
		} finally { if (token === seq) uploading = false; }
	}
</script>

<PageHeader title="New expense" back={{ href: `/${org}/expenses`, label: 'Back to expenses' }} />
{#if !allowed}<p role="alert">Expenses are unavailable for this organisation.</p>
{:else}
	<p class="mb-4">Record a paid expense without a bank import. If it appears in a later bank import, match the existing expense rather than creating it again.</p>
	<LoadState loading={loaded !== optionScope} {error} loadingLabel="Loading expense options…" onretry={load}>
		{#if savedID !== null}<p role="status" class="alert alert-success alert-soft mb-3">Expense saved. <a class="link" href={`/${org}/expenses/${savedID}`}>View saved expense</a></p>{/if}
		{#each warnings as warning}<p role="status" class="alert alert-warning alert-soft mb-3">{warning}</p>{/each}
		{#if !categories.some((c) => c.type === 'expense')}<p>No expense categories available. Add an expense category before recording expenses.</p>
		{:else}
			<div class="max-w-3xl rounded-box border border-base-300 bg-base-100 p-4">
				{#key `${optionScope}:${version}`}<ExpenseForm lineDate={todayLocal()} {categories} {suppliers} {properties} {projects} {initialPropertyId} busy={saving} error={saveError} onSubmit={save} onReceiptUpload={uploadReceipt} onchange={() => dirty = true} oncancel={() => { if (allowLeave()) { dirty = false; goto(`/${org}/expenses`); } }} />{/key}
			</div>
		{/if}
	</LoadState>
{/if}
