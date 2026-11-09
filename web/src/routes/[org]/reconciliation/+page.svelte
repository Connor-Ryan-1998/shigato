<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import FilterBar from '../../../lib/components/FilterBar.svelte';
	import Pagination from '../../../lib/components/Pagination.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import EmptyState from '../../../lib/components/EmptyState.svelte';
	import Toast from '../../../lib/components/Toast.svelte';
	import BankLineTable, { type BankLine } from '../../../lib/modules/reconciliation/BankLineTable.svelte';
	import LineDrawer from '../../../lib/modules/reconciliation/LineDrawer.svelte';
	import type { Category, SimpleOption } from '../../../lib/modules/reconciliation/ExpenseForm.svelte';

	const PAGE_SIZE = 50;

	const TABS: { label: string; value: string }[] = [
		{ label: 'Unmatched', value: 'unmatched' },
		{ label: 'Matched', value: 'matched' },
		{ label: 'Ignored', value: 'ignored' },
		{ label: 'All', value: '' }
	];

	type BankAccount = { id: number; name: string };
	type ListResponse = { lines: BankLine[]; total: number };

	let lines = $state<BankLine[]>([]);
	let total = $state(0);
	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let requestSeq = 0;
	// True only once the list has rendered at least once. Every later
	// load() (a tab/filter/offset change, or a refresh after a drawer
	// action) then becomes a background refresh - refreshing stays true
	// while the already-rendered table and its rows stay mounted, instead
	// of tearing the whole view down to a bare "Loading…" line for every
	// single action.
	let hasLoadedOnce = false;
	let refreshing = $state(false);

	let accounts = $state<BankAccount[]>([]);
	let categories = $state<Category[]>([]);
	let suppliers = $state<SimpleOption[]>([]);
	let properties = $state<SimpleOption[] | undefined>(undefined);
	let projects = $state<SimpleOption[] | undefined>(undefined);

	let toastMessage = $state('');
	let toastTone = $state<'success' | 'danger'>('success');

	let drawerOpen = $state(false);
	let selectedLine = $state<BankLine | null>(null);

	const activeTab = $derived(page.url.searchParams.get('status') ?? 'unmatched');
	const accountFilter = $derived(page.url.searchParams.get('account_id') ?? '');
	const fromFilter = $derived(page.url.searchParams.get('from') ?? '');
	const toFilter = $derived(page.url.searchParams.get('to') ?? '');
	const offset = $derived(Math.max(0, Number(page.url.searchParams.get('offset') ?? '0') || 0));

	function tabHref(value: string): string {
		const params = new URLSearchParams(page.url.search);
		params.delete('offset');
		if (value === '') {
			params.delete('status');
		} else {
			params.set('status', value);
		}
		const qs = params.toString();
		return `/${page.params.org}/reconciliation${qs ? `?${qs}` : ''}`;
	}

	function updateParam(key: string, value: string) {
		const params = new URLSearchParams(page.url.search);
		params.delete('offset');
		if (value === '') {
			params.delete(key);
		} else {
			params.set(key, value);
		}
		const qs = params.toString();
		goto(`/${page.params.org}/reconciliation${qs ? `?${qs}` : ''}`);
	}

	async function load() {
		const seq = ++requestSeq;
		if (hasLoadedOnce) {
			refreshing = true;
		} else {
			loadStatus = 'loading';
		}
		errorMessage = '';
		try {
			const org = page.params.org;
			const params = new URLSearchParams();
			if (activeTab !== '') params.set('status', activeTab);
			if (accountFilter !== '') params.set('account_id', accountFilter);
			if (fromFilter !== '') params.set('from', fromFilter);
			if (toFilter !== '') params.set('to', toFilter);
			params.set('limit', String(PAGE_SIZE));
			params.set('offset', String(offset));
			const res = await api<ListResponse>('GET', `/api/v1/orgs/${org}/bank-lines?${params}`);
			if (seq !== requestSeq) return;
			lines = res.lines;
			total = res.total;
			loadStatus = 'ready';
			hasLoadedOnce = true;
		} catch (e) {
			if (seq !== requestSeq) return;
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			// Once the table has rendered once, a failed refresh (a bad
			// filter, a dropped connection) shows inline above the still-
			// mounted table instead of replacing it with the full-page
			// error state, which would otherwise discard the user's place.
			if (!hasLoadedOnce) {
				loadStatus = 'error';
			}
		} finally {
			refreshing = false;
		}
	}

	$effect(() => {
		page.url.search;
		load();
	});

	async function loadSupportingData() {
		const org = page.params.org;
		try {
			accounts = await api<BankAccount[]>('GET', `/api/v1/orgs/${org}/bank-accounts`);
		} catch {
			// The account filter simply stays empty; the page itself still works.
		}
		try {
			categories = await api<Category[]>('GET', `/api/v1/orgs/${org}/categories`);
		} catch {
			// Expense creation will show a server error when it tries to save.
		}
		try {
			suppliers = await api<{ id: number; name: string }[]>(
				'GET',
				`/api/v1/orgs/${org}/contacts?kind=supplier`
			);
		} catch {
			// Supplier stays optional either way.
		}
		if (session.modules.includes('property')) {
			try {
				properties = await api<SimpleOption[]>('GET', `/api/v1/orgs/${org}/properties`);
			} catch {
				properties = [];
			}
		}
		if (session.modules.includes('timesheets')) {
			try {
				projects = await api<SimpleOption[]>('GET', `/api/v1/orgs/${org}/projects`);
			} catch {
				projects = [];
			}
		}
	}

	onMount(loadSupportingData);

	function gotoOffset(next: number) {
		const params = new URLSearchParams(page.url.search);
		if (next > 0) {
			params.set('offset', String(next));
		} else {
			params.delete('offset');
		}
		const qs = params.toString();
		goto(`/${page.params.org}/reconciliation${qs ? `?${qs}` : ''}`);
	}

	function selectLine(line: BankLine) {
		selectedLine = line;
		drawerOpen = true;
	}

	function onLineChanged() {
		load();
	}

	let autoMatchBusy = $state(false);

	async function autoMatch() {
		if (autoMatchBusy) return;
		autoMatchBusy = true;
		try {
			const org = page.params.org;
			const res = await api<{ matched: number }>('POST', `/api/v1/orgs/${org}/reconcile/auto`);
			toastTone = 'success';
			toastMessage = `Auto-matched ${res.matched} line${res.matched === 1 ? '' : 's'}.`;
			await load();
		} catch (e) {
			toastTone = 'danger';
			toastMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			autoMatchBusy = false;
		}
	}

	async function uploadReceipt(file: File): Promise<number> {
		const org = page.params.org;
		const body = new FormData();
		body.append('file', file);
		const created = await api<{ id: number }>('POST', `/api/v1/orgs/${org}/files`, body);
		return created.id;
	}
</script>

<PageHeader title="Reconciliation">
	{#snippet actions()}
		<Button variant="secondary" onclick={() => goto(`/${page.params.org}/reconciliation/accounts`)}>
			Bank accounts
		</Button>
		<Button disabled={autoMatchBusy} onclick={autoMatch}>Auto-match</Button>
	{/snippet}
</PageHeader>

<nav class="tabs tabs-border mb-4" aria-label="Bank line status">
	{#each TABS as tab (tab.value)}
		<a href={tabHref(tab.value)} class={['tab', activeTab === tab.value && 'tab-active']} aria-current={activeTab === tab.value ? 'page' : undefined}>
			{tab.label}
		</a>
	{/each}
</nav>

<FilterBar>
	<Field label="Account" id="account-filter">
		<Select
			id="account-filter"
			value={accountFilter}
			options={[
				{ value: '', label: 'All accounts' },
				...accounts.map((a) => ({ value: String(a.id), label: a.name }))
			]}
			onchange={(v) => updateParam('account_id', v)}
		/>
	</Field>
	<Field label="From" id="from-filter">
		<input
			class="input w-full"
			id="from-filter"
			type="date"
			value={fromFilter}
			onchange={(e) => updateParam('from', (e.currentTarget as HTMLInputElement).value)}
		/>
	</Field>
	<Field label="To" id="to-filter">
		<input
			class="input w-full"
			id="to-filter"
			type="date"
			value={toFilter}
			onchange={(e) => updateParam('to', (e.currentTarget as HTMLInputElement).value)}
		/>
	</Field>
</FilterBar>

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading bank lines…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load bank lines" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else}
	{#if refreshing}
		<p class="mb-3 text-sm text-base-content/70" aria-live="polite">Refreshing…</p>
	{:else if errorMessage}
		<p class="alert alert-error alert-soft mb-3" role="alert">{errorMessage}</p>
	{/if}
	{#if lines.length === 0}
		<EmptyState title="No bank lines in this view" />
	{:else}
		<BankLineTable {lines} onSelect={selectLine} />
		<Pagination {offset} pageSize={PAGE_SIZE} {total} count={lines.length} onchange={gotoOffset} />
	{/if}
{/if}

<LineDrawer
	bind:open={drawerOpen}
	org={page.params.org ?? ''}
	line={selectedLine}
	{categories}
	{suppliers}
	{properties}
	{projects}
	onReceiptUpload={uploadReceipt}
	onChanged={onLineChanged}
/>

{#if toastMessage}
	<Toast message={toastMessage} tone={toastTone} onDismiss={() => (toastMessage = '')} />
{/if}
