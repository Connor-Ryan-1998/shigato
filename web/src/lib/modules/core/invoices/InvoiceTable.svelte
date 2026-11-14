<script lang="ts" module>
	// InvoiceListItem is the subset of GET /invoices' per-item fields this
	// table shows. List items never carry payments/provider_url (detail-only
	// fields - see internal/billing/invoice_response.go).
	export type InvoiceListItem = {
		id: number;
		number: number | null;
		contact_name: string;
		issue_date: string | null;
		due_date: string | null;
		total: number;
		outstanding: number;
		status: string;
	};
</script>

<script lang="ts">
	import DataTable from '../../../components/DataTable.svelte';
	import Money from '../../../components/Money.svelte';
	import DateText from '../../../components/DateText.svelte';
	import StatusBadge from '../../../components/StatusBadge.svelte';

	let { org, invoices }: { org: string; invoices: InvoiceListItem[] } = $props();

	const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
		draft: 'info',
		open: 'warning',
		paid: 'success',
		void: 'danger',
		uncollectible: 'danger'
	};
</script>

{#snippet numberCell(row: InvoiceListItem)}
	<a href={`/${org}/invoices/${row.id}`}>{row.number ?? 'Draft'}</a>
{/snippet}

{#snippet issueDateCell(row: InvoiceListItem)}
	{#if row.issue_date}<DateText iso={row.issue_date} />{:else}—{/if}
{/snippet}

{#snippet dueDateCell(row: InvoiceListItem)}
	{#if row.due_date}<DateText iso={row.due_date} />{:else}—{/if}
{/snippet}

{#snippet totalCell(row: InvoiceListItem)}<Money cents={row.total} />{/snippet}

{#snippet outstandingCell(row: InvoiceListItem)}<Money cents={row.outstanding} />{/snippet}

{#snippet statusCell(row: InvoiceListItem)}
	<StatusBadge status={row.status} tone={STATUS_TONE[row.status] ?? 'info'} />
{/snippet}

<DataTable
	columns={[
		{ key: 'number', label: 'Number', cell: numberCell },
		{ key: 'contact_name', label: 'Contact' },
		{ key: 'issue_date', label: 'Issue date', cell: issueDateCell },
		{ key: 'due_date', label: 'Due date', cell: dueDateCell },
		{ key: 'total', label: 'Total', cell: totalCell },
		{ key: 'outstanding', label: 'Outstanding', cell: outstandingCell },
		{ key: 'status', label: 'Status', cell: statusCell }
	]}
	rows={invoices}
	rowKey={(row) => row.id}
/>
