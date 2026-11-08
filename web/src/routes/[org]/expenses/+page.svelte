<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import { money, todayLocal } from '../../../lib/format';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import DataTable from '../../../lib/components/DataTable.svelte';
	import ExpenseForm, { type Category, type SimpleOption, type NewExpense } from '../../../lib/modules/reconciliation/ExpenseForm.svelte';
	import type { Expense } from '../../../lib/modules/reconciliation/expenses';

	const org = $derived(page.params.org);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(session.modules.includes('reconciliation') && (membership?.perms.includes('reports_read') ?? false));
	const canWrite = $derived(canRead && (membership?.perms.includes('recon_write') ?? false));
	const canReadContacts = $derived(membership?.perms.includes('invoices_read') ?? false);
	const propertyEnabled = $derived(session.modules.includes('property'));
	const timesheetsEnabled = $derived(session.modules.includes('timesheets'));
	let from = $state('');
	let to = $state('');
	const listKey = $derived(`${org}:${from}:${to}`);
	let expenses = $state<Expense[]>([]);
	let loadedKey = $state('');
	let listError = $state('');
	let listSeq = 0;
	let categories = $state<Category[]>([]);
	let suppliers = $state<SimpleOption[]>([]);
	let properties = $state<SimpleOption[] | undefined>();
	let projects = $state<SimpleOption[] | undefined>();
	let optionsOrg = $state('');
	let optionsError = $state('');
	let optionsSeq = 0;
	let formOrg = $state('');
	let formVersion = $state(0);
	let saving = $state(false);
	let saveError = $state('');
	let savedID = $state<number | null>(null);
	let warnings = $state<string[]>([]);
	let actionSeq = 0;
	const today = todayLocal();

	async function load(slug: string, start: string, end: string) {
		const seq = ++listSeq;
		const key = `${slug}:${start}:${end}`;
		expenses = [];
		loadedKey = '';
		listError = '';
		if (start && end && start > end) {
			listError = 'From must not be after To.';
			loadedKey = key;
			return;
		}
		const params = new URLSearchParams();
		if (start) params.set('from', start);
		if (end) params.set('to', end);
		try {
			// shortcut: existing API is unpaginated; add server pagination when date-filtered ledgers outgrow it.
			const result = await api<Expense[]>('GET', `/api/v1/orgs/${slug}/expenses${params.size ? `?${params}` : ''}`);
			if (seq !== listSeq || key !== listKey || !canRead) return;
			expenses = result;
		} catch (e) {
			if (seq !== listSeq || key !== listKey || !canRead) return;
			listError = e instanceof ApiError ? e.message : 'Could not load expenses.';
		} finally {
			if (seq === listSeq && key === listKey && canRead) loadedKey = key;
		}
	}

	async function loadOptions(slug: string) {
		const seq = ++optionsSeq;
		optionsOrg = '';
		optionsError = '';
		try {
			const result = await Promise.all([
				api<Category[]>('GET', `/api/v1/orgs/${slug}/categories`),
				canReadContacts ? api<SimpleOption[]>('GET', `/api/v1/orgs/${slug}/contacts?kind=supplier`) : Promise.resolve([]),
				propertyEnabled ? api<SimpleOption[]>('GET', `/api/v1/orgs/${slug}/properties`) : Promise.resolve(undefined),
				timesheetsEnabled ? api<SimpleOption[]>('GET', `/api/v1/orgs/${slug}/projects`) : Promise.resolve(undefined)
			]);
			if (seq !== optionsSeq || slug !== org || !canRead) return;
			[categories, suppliers, properties, projects] = result;
			optionsOrg = slug;
		} catch (e) {
			if (seq !== optionsSeq || slug !== org || !canRead) return;
			optionsError = e instanceof ApiError ? e.message : 'Could not load expense options.';
		}
	}

	$effect(() => {
		if (canRead && org) load(org, from, to);
		return () => { listSeq++; };
	});
	$effect(() => {
		if (canRead && org) loadOptions(org);
		return () => { optionsSeq++; };
	});
	$effect(() => {
		formOrg = canWrite ? org ?? '' : '';
		formVersion = 0;
		saving = false;
		saveError = '';
		savedID = null;
		warnings = [];
		actionSeq++;
	});

	async function save(expense: NewExpense) {
		if (!canWrite || !org || formOrg !== org || optionsOrg !== org || saving) return;
		const slug = org;
		const seq = ++actionSeq;
		saving = true;
		saveError = '';
		savedID = null;
		warnings = [];
		try {
			const result = await api<Expense & { warnings?: string[] }>('POST', `/api/v1/orgs/${slug}/expenses`, expense);
			if (seq !== actionSeq || slug !== org || !canWrite) return;
			savedID = result.id;
			warnings = result.warnings ?? [];
			formVersion++;
			await load(slug, from, to);
		} catch (e) {
			if (seq !== actionSeq || slug !== org || !canWrite) return;
			saveError = e instanceof ApiError ? e.message : 'Could not save expense.';
		} finally {
			if (seq === actionSeq) saving = false;
		}
	}

	async function uploadReceipt(file: File) {
		const slug = org;
		const seq = actionSeq;
		if (!canWrite || !slug || formOrg !== slug || saving) throw new Error('Expense form is no longer available.');
		const body = new FormData();
		body.append('file', file);
		const result = await api<{ id: number }>('POST', `/api/v1/orgs/${slug}/files`, body);
		if (seq !== actionSeq || slug !== org || !canWrite) throw new Error('Expense form has changed.');
		return result.id;
	}
</script>

<PageHeader title="Expenses" />
{#snippet descriptionCell(expense: Expense)}<a href={`/${org}/expenses/${expense.id}`}>{expense.description}</a>{/snippet}
{#snippet totalCell(expense: Expense)}{money(expense.total)}{/snippet}
{#snippet supplierCell(expense: Expense)}{expense.supplier_contact_id ? (optionsOrg === org ? suppliers.find((s) => s.id === expense.supplier_contact_id)?.name : undefined) ?? `Supplier #${expense.supplier_contact_id}` : 'No supplier'}{/snippet}
{#snippet statusCell(expense: Expense)}{expense.bank_line_id ? 'Matched to bank' : 'Not matched to bank'}{/snippet}

{#if !canRead}
	<p role="alert">Expenses are unavailable for this organisation.</p>
{:else}
	<div class="filters">
		<Field label="From" id="expenses-from"><input id="expenses-from" type="date" bind:value={from} /></Field>
		<Field label="To" id="expenses-to"><input id="expenses-to" type="date" bind:value={to} /></Field>
	</div>
	{#if loadedKey !== listKey}
		<p role="status">Loading expenses…</p>
	{:else if listError}
		<p role="alert">{listError}</p>
		<Button onclick={() => org && load(org, from, to)}>Retry expenses</Button>
	{:else if expenses.length === 0}
		<p>No expenses found.</p>
	{:else}
		<DataTable columns={[
			{ key: 'spent_on', label: 'Spent on' }, { key: 'description', label: 'Description', cell: descriptionCell },
			{ key: 'supplier', label: 'Supplier', cell: supplierCell }, { key: 'total', label: 'Total', cell: totalCell },
			{ key: 'status', label: 'Bank match', cell: statusCell }
		]} rows={expenses} rowKey={(expense) => expense.id} />
	{/if}
	{#if canWrite && formOrg === org}
		<section>
			<h2>New expense</h2>
			<p>Record a paid expense without a bank import. If it appears in a later bank import, match the existing expense rather than creating it again.</p>
			{#if savedID !== null}
				<p role="status">Expense saved. <a href={`/${org}/expenses/${savedID}`}>View saved expense</a></p>
				{#each warnings as warning}<p role="status">{warning}</p>{/each}
			{/if}
			{#if optionsError}
				<p role="alert">{optionsError}</p>
				<Button onclick={() => org && loadOptions(org)}>Retry expense options</Button>
			{:else if optionsOrg !== org}
				<p role="status">Loading expense options…</p>
			{:else if !categories.some((c) => c.type === 'expense')}
				<p>No expense categories available. Add an expense category before recording expenses.</p>
			{:else}
				{#key `${org}:${formVersion}`}
					<ExpenseForm lineDate={today} {categories} {suppliers} {properties} {projects} busy={saving} error={saveError || undefined} onSubmit={save} onReceiptUpload={uploadReceipt} />
				{/key}
			{/if}
		</section>
	{/if}
{/if}

<style>
	.filters { display: flex; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-4); }
	section { margin-top: var(--space-6); }
</style>
