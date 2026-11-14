<script lang="ts" module>
	// Shape of the dashboard response's "property" section
	// (internal/property/routes.go's propertyDashboard).
	export type PropertyDashboard = {
		arrears_total: number;
		unissued_total: number;
		bonds_unlodged_overdue: number;
		insurance_renewing_soon: number;
	};
</script>

<script lang="ts">
	import Money from '../../components/Money.svelte';

	let { data }: { data: PropertyDashboard } = $props();
</script>

<section class="card card-border bg-base-100 p-4 shadow-sm">
	<h2 class="mb-3 text-lg font-bold">Property</h2>
	<dl class="m-0">
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Arrears</dt>
			<dd class="m-0 font-semibold tabular-nums"><Money cents={data.arrears_total} /></dd>
		</div>
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Unissued</dt>
			<dd class="m-0 font-semibold tabular-nums"><Money cents={data.unissued_total} /></dd>
		</div>
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Bonds overdue to lodge</dt>
			<dd class="m-0 font-semibold tabular-nums">{data.bonds_unlodged_overdue}</dd>
		</div>
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Insurance renewing within 30 days</dt>
			<dd class="m-0 font-semibold tabular-nums">{data.insurance_renewing_soon}</dd>
		</div>
	</dl>
</section>
