<script lang="ts" module>
	export type BankLineMatch = { kind: string; id: number; label: string };

	export type BankLine = {
		id: number;
		bank_account_id: number;
		import_id: number;
		posted_on: string;
		amount: number;
		description: string;
		status: string;
		ignore_reason?: string;
		match: BankLineMatch | null;
	};
</script>

<script lang="ts">
	import DataTable from '../../components/DataTable.svelte';
	import DateText from '../../components/DateText.svelte';
	import Money from '../../components/Money.svelte';
	import StatusBadge from '../../components/StatusBadge.svelte';

	let { lines, onSelect }: { lines: BankLine[]; onSelect: (line: BankLine) => void } = $props();

	const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
		unmatched: 'warning',
		matched: 'success',
		ignored: 'info'
	};

	function statusLabel(line: BankLine): string {
		if (line.match) return `${line.match.kind}: ${line.match.label}`;
		return line.status;
	}
</script>

{#snippet dateCell(line: BankLine)}<DateText iso={line.posted_on} />{/snippet}
{#snippet descCell(line: BankLine)}{line.description}{/snippet}
{#snippet amountCell(line: BankLine)}
	<span class="amount" class:credit={line.amount > 0} class:debit={line.amount < 0}>
		{line.amount > 0 ? '+' : line.amount < 0 ? '−' : ''}<Money cents={Math.abs(line.amount)} />
	</span>
{/snippet}
{#snippet statusCell(line: BankLine)}
	<StatusBadge status={statusLabel(line)} tone={STATUS_TONE[line.status] ?? 'info'} />
{/snippet}
{#snippet rowButtonCell(line: BankLine)}
	<button type="button" class="row-link" onclick={() => onSelect(line)}>View</button>
{/snippet}

<DataTable
	columns={[
		{ key: 'posted_on', label: 'Date', cell: dateCell },
		{ key: 'description', label: 'Description', cell: descCell },
		{ key: 'amount', label: 'Amount', cell: amountCell },
		{ key: 'status', label: 'Status', cell: statusCell },
		{ key: 'actions', label: 'Actions', cell: rowButtonCell }
	]}
	rows={lines}
	rowKey={(line) => line.id}
/>

<style>
	.amount {
		font-variant-numeric: tabular-nums;
	}
	.credit {
		color: var(--success);
	}
	.debit {
		color: var(--danger);
	}
	.row-link {
		background: none;
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
		padding: var(--space-1) var(--space-3);
		cursor: pointer;
		color: var(--text);
		font: inherit;
	}
	.row-link:hover {
		background: var(--surface-raised);
	}
</style>
