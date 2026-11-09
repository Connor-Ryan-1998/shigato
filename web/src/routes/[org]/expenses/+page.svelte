<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import { money, date } from '../../../lib/format';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import FilterBar from '../../../lib/components/FilterBar.svelte';
	import LoadState from '../../../lib/components/LoadState.svelte';
	import DataTable from '../../../lib/components/DataTable.svelte';
	import type { Expense } from '../../../lib/modules/reconciliation/expenses';
	const org = $derived(page.params.org ?? '');
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(session.modules.includes('reconciliation') && (membership?.perms.includes('reports_read') ?? false));
	const canWrite = $derived(canRead && (membership?.perms.includes('recon_write') ?? false));
	let from = $state('');
	let to = $state('');
	let expenses = $state<Expense[]>([]);
	let suppliers = $state<{ id: number; name: string }[]>([]);
	let loadedKey = $state('');
	let error = $state('');
	let seq = 0;
	const key = $derived(`${org}:${from}:${to}`);
	async function load(slug: string, start: string, end: string) {
		const token = ++seq;
		const scope = `${slug}:${start}:${end}`;
		error = '';
		if (start && end && start > end) { error = 'From must not be after To.'; return; }
		const params = new URLSearchParams();
		if (start) params.set('from', start);
		if (end) params.set('to', end);
		try {
			// shortcut: existing API is unpaginated; add server pagination when date-filtered ledgers outgrow it.
			const result = await api<Expense[]>('GET', `/api/v1/orgs/${slug}/expenses${params.size ? `?${params}` : ''}`);
			if (token !== seq || scope !== key || !canRead) return;
			expenses = result; loadedKey = scope;
		} catch (e) { if (token === seq && scope === key) error = e instanceof ApiError ? e.message : 'Could not load expenses.'; }
	}
	$effect(() => { if (canRead && org) load(org, from, to); return () => { seq++; }; });
	$effect(() => {
		const slug = org;
		let active = true;
		suppliers = [];
		if (canRead && membership?.perms.includes('invoices_read')) api<typeof suppliers>('GET', `/api/v1/orgs/${slug}/contacts?kind=supplier`).then((result) => { if (active && slug === org) suppliers = result; }).catch(() => {});
		return () => { active = false; };
	});
</script>

<PageHeader title="Expenses">{#snippet actions()}{#if canWrite}<a class="btn btn-primary" href={`/${org}/expenses/new`}>New expense</a>{/if}{/snippet}</PageHeader>
{#snippet descriptionCell(expense: Expense)}<a class="link link-primary" href={`/${org}/expenses/${expense.id}`}>{expense.description}</a>{/snippet}
{#snippet dateCell(expense: Expense)}{date(expense.spent_on)}{/snippet}
{#snippet totalCell(expense: Expense)}{money(expense.total)}{/snippet}
{#snippet supplierCell(expense: Expense)}{expense.supplier_contact_id ? suppliers.find((s) => s.id === expense.supplier_contact_id)?.name ?? `Supplier #${expense.supplier_contact_id}` : 'No supplier'}{/snippet}
{#snippet statusCell(expense: Expense)}{expense.bank_line_id ? 'Matched to bank' : 'Not matched to bank'}{/snippet}
{#if !canRead}<p role="alert">Expenses are unavailable for this organisation.</p>
{:else}
	<FilterBar>
		<Field label="From" id="expenses-from"><input class="input" id="expenses-from" type="date" bind:value={from} /></Field>
		<Field label="To" id="expenses-to"><input class="input" id="expenses-to" type="date" bind:value={to} /></Field>
	</FilterBar>
	<LoadState loading={loadedKey !== key} {error} empty={expenses.length === 0} emptyTitle="No expenses found." loadingLabel="Loading expenses…" onretry={() => load(org, from, to)}>
		<DataTable columns={[{ key: 'spent_on', label: 'Spent on', cell: dateCell }, { key: 'description', label: 'Description', cell: descriptionCell }, { key: 'supplier', label: 'Supplier', cell: supplierCell }, { key: 'total', label: 'Total', cell: totalCell, align: 'right' }, { key: 'status', label: 'Bank match', cell: statusCell }]} rows={expenses} rowKey={(expense) => expense.id} />
	</LoadState>
{/if}
