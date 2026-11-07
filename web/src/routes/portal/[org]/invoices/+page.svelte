<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import EmptyState from '../../../../lib/components/EmptyState.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import DataTable from '../../../../lib/components/DataTable.svelte';
	import StatusBadge from '../../../../lib/components/StatusBadge.svelte';
	import Money from '../../../../lib/components/Money.svelte';
	import DateText from '../../../../lib/components/DateText.svelte';

	type InvoiceListItem = {
		id: number;
		number: number | null;
		status: string;
		due_date: string | null;
		total: number;
		outstanding: number;
		payment_reference: string;
	};

	// Draft and void are never returned by the portal API
	// (global-constraints.md "Portal isolation"), so no tone is needed for
	// them.
	const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
		open: 'warning',
		paid: 'success',
		uncollectible: 'danger'
	};

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let invoices = $state<InvoiceListItem[]>([]);

	async function load() {
		loadStatus = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			invoices = await api<InvoiceListItem[]>('GET', `/api/v1/portal/${org}/invoices`);
			loadStatus = 'ready';
		} catch (e) {
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	$effect(() => {
		page.params.org;
		load();
	});
</script>

{#snippet numberCell(inv: InvoiceListItem)}
	<a href={`/portal/${page.params.org}/invoices/${inv.id}`}>
		{inv.number !== null ? `#${inv.number}` : 'Invoice'}
	</a>
{/snippet}
{#snippet statusCell(inv: InvoiceListItem)}
	<StatusBadge status={inv.status} tone={STATUS_TONE[inv.status] ?? 'info'} />
{/snippet}
{#snippet dueCell(inv: InvoiceListItem)}
	{#if inv.due_date}<DateText iso={inv.due_date} />{/if}
{/snippet}
{#snippet totalCell(inv: InvoiceListItem)}<Money cents={inv.total} />{/snippet}
{#snippet outstandingCell(inv: InvoiceListItem)}<Money cents={inv.outstanding} />{/snippet}
{#snippet referenceCell(inv: InvoiceListItem)}{inv.payment_reference}{/snippet}

<PageHeader title="Invoices" />

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading invoices…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load invoices" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if invoices.length === 0}
	<EmptyState title="No invoices yet" />
{:else}
	<DataTable
		columns={[
			{ key: 'number', label: 'Invoice', cell: numberCell },
			{ key: 'status', label: 'Status', cell: statusCell },
			{ key: 'due_date', label: 'Due', cell: dueCell },
			{ key: 'total', label: 'Total', cell: totalCell },
			{ key: 'outstanding', label: 'Outstanding', cell: outstandingCell },
			{ key: 'payment_reference', label: 'Reference', cell: referenceCell }
		]}
		rows={invoices}
		rowKey={(inv) => inv.id}
	/>
{/if}
