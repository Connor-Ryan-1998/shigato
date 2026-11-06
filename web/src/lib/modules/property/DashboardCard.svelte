<script lang="ts" module>
	// Shape of the dashboard response's "property" section
	// (internal/property/routes.go's propertyDashboard).
	export type PropertyDashboard = {
		arrears_total: number;
		unissued_total: number;
		bonds_unlodged_overdue: number;
	};
</script>

<script lang="ts">
	import Money from '../../components/Money.svelte';

	let { data }: { data: PropertyDashboard } = $props();
</script>

<section class="card">
	<h2>Property</h2>
	<dl>
		<div class="row">
			<dt>Arrears</dt>
			<dd><Money cents={data.arrears_total} /></dd>
		</div>
		<div class="row">
			<dt>Unissued</dt>
			<dd><Money cents={data.unissued_total} /></dd>
		</div>
		<div class="row">
			<dt>Bonds overdue to lodge</dt>
			<dd>{data.bonds_unlodged_overdue}</dd>
		</div>
	</dl>
</section>

<style>
	.card {
		padding: var(--space-4);
		border-radius: var(--radius-lg);
		background: var(--surface);
		border: 1px solid var(--border);
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
