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

<a class="card card-border bg-base-100 p-4 shadow-sm hover:bg-base-200" href={`/${org}/reconciliation`}>
	<h2 class="mb-3 text-lg font-bold">Reconciliation</h2>
	<dl class="m-0">
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Unmatched lines</dt>
			<dd class="m-0 font-semibold tabular-nums">{data.unmatched_count}</dd>
		</div>
		{#if data.oldest_unmatched_on}
			<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
				<dt class="text-base-content/70">Oldest unmatched</dt>
				<dd class="m-0 font-semibold tabular-nums"><DateText iso={data.oldest_unmatched_on} /></dd>
			</div>
		{/if}
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Unmatched in / out</dt>
			<dd class="m-0 font-semibold tabular-nums"><Money cents={data.unmatched_in} /> / <Money cents={data.unmatched_out} /></dd>
		</div>
	</dl>
</a>
