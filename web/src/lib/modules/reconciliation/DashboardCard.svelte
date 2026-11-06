<script lang="ts" module>
	// Shape of the dashboard response's "reconciliation" section
	// (internal/recon/routes.go's reconDashboard). oldest_unmatched_on is
	// null when there are no unmatched lines at all.
	export type ReconciliationDashboard = {
		unmatched_count: number;
		oldest_unmatched_on: string | null;
		unmatched_in: number;
		unmatched_out: number;
	};
</script>

<script lang="ts">
	import Money from '../../components/Money.svelte';
	import DateText from '../../components/DateText.svelte';

	// org is the current org's slug, for an absolute (org-scoped) link.
	let { data, org }: { data: ReconciliationDashboard; org: string } = $props();
</script>

<a class="card" href={`/${org}/reconciliation`}>
	<h2>Reconciliation</h2>
	<dl>
		<div class="row">
			<dt>Unmatched lines</dt>
			<dd>{data.unmatched_count}</dd>
		</div>
		{#if data.oldest_unmatched_on}
			<div class="row">
				<dt>Oldest unmatched</dt>
				<dd><DateText iso={data.oldest_unmatched_on} /></dd>
			</div>
		{/if}
		<div class="row">
			<dt>Unmatched in / out</dt>
			<dd><Money cents={data.unmatched_in} /> / <Money cents={data.unmatched_out} /></dd>
		</div>
	</dl>
</a>

<style>
	.card {
		display: block;
		padding: var(--space-4);
		border-radius: var(--radius-lg);
		background: var(--surface);
		border: 1px solid var(--border);
		color: inherit;
		text-decoration: none;
	}
	.card:hover {
		background: var(--surface-raised);
	}
	h2 {
		margin: 0 0 var(--space-3);
		font-size: var(--text-lg);
	}
	dl {
		margin: 0;
	}
	.row {
		display: flex;
		justify-content: space-between;
		gap: var(--space-4);
		padding: var(--space-2) 0;
		border-top: 1px solid var(--border);
	}
	.row:first-child {
		border-top: none;
	}
	dt {
		color: var(--text-muted);
	}
	dd {
		margin: 0;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}
</style>
