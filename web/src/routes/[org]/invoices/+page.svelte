<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import EmptyState from '../../../lib/components/EmptyState.svelte';
	import Pagination from '../../../lib/components/Pagination.svelte';
	import InvoiceTable, {
		type InvoiceListItem
	} from '../../../lib/modules/core/invoices/InvoiceTable.svelte';

	const PAGE_SIZE = 50;

	// The Overdue tab is held the same way as the others (?status=overdue in
	// the URL), but translated to the API's own overdue=true filter (there's
	// no "overdue" invoice status - it's "open" with a past due date; see
	// core.InvoiceListFilter.Overdue).
	const TABS: { label: string; value: string }[] = [
		{ label: 'All', value: '' },
		{ label: 'Draft', value: 'draft' },
		{ label: 'Open', value: 'open' },
		{ label: 'Overdue', value: 'overdue' },
		{ label: 'Paid', value: 'paid' },
		{ label: 'Void', value: 'void' }
	];

	type ListResponse = { invoices: InvoiceListItem[]; total: number };

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let invoices = $state<InvoiceListItem[]>([]);
	let total = $state(0);
	// Bumped on every load() call; a response is only applied if it's still
	// the most recent request by the time it resolves - otherwise a slow
	// earlier request (e.g. the previous tab) could land after a faster
	// later one and clobber it with stale data.
	let requestSeq = 0;

	const activeTab = $derived(page.url.searchParams.get('status') ?? '');
	const offset = $derived(Math.max(0, Number(page.url.searchParams.get('offset') ?? '0') || 0));

	function tabHref(value: string): string {
		return value === '' ? `/${page.params.org}/invoices` : `/${page.params.org}/invoices?status=${value}`;
	}

	async function load() {
		const seq = ++requestSeq;
		loadStatus = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			const params = new URLSearchParams();
			if (activeTab === 'overdue') {
				params.set('overdue', 'true');
			} else if (activeTab !== '') {
				params.set('status', activeTab);
			}
			params.set('limit', String(PAGE_SIZE));
			params.set('offset', String(offset));
			const res = await api<ListResponse>('GET', `/api/v1/orgs/${org}/invoices?${params}`);
			if (seq !== requestSeq) return;
			invoices = res.invoices;
			total = res.total;
			loadStatus = 'ready';
		} catch (e) {
			if (seq !== requestSeq) return;
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	// Re-fetches whenever the tab or page (offset) changes in the URL.
	// Reading page.url.search directly (rather than only inside load, which
	// is async) guarantees it's tracked before load's first await.
	$effect(() => {
		page.url.search;
		load();
	});

	function gotoOffset(next: number) {
		const params = new URLSearchParams(page.url.search);
		if (next > 0) {
			params.set('offset', String(next));
		} else {
			params.delete('offset');
		}
		const qs = params.toString();
		goto(`/${page.params.org}/invoices${qs ? `?${qs}` : ''}`);
	}
</script>

<PageHeader title="Invoices">
	{#snippet actions()}
		<Button onclick={() => goto(`/${page.params.org}/invoices/new`)}>New invoice</Button>
	{/snippet}
</PageHeader>

<nav class="tabs tabs-border mb-4" aria-label="Invoice status">
	{#each TABS as tab (tab.value)}
		<a
			href={tabHref(tab.value)}
			class={['tab', activeTab === tab.value && 'tab-active']}
			aria-current={activeTab === tab.value ? 'page' : undefined}
		>
			{tab.label}
		</a>
	{/each}
</nav>

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading invoices…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load invoices" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if invoices.length === 0 && offset > 0}
	<EmptyState title="No more invoices" description="You've gone past the last page.">
		{#snippet action()}
			<Button onclick={() => gotoOffset(0)}>Back to first page</Button>
		{/snippet}
	</EmptyState>
{:else if invoices.length === 0}
	<EmptyState title="No invoices in this view" />
{:else}
	<InvoiceTable org={page.params.org ?? ''} {invoices} />
	<Pagination {offset} pageSize={PAGE_SIZE} {total} count={invoices.length} onchange={gotoOffset} />
{/if}
