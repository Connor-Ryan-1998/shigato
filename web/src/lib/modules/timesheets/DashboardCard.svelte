<script lang="ts" module>
	// Shape of the dashboard response's "timesheets" section
	// (internal/timesheets/routes.go's timesheetsDashboard).
	export type TimesheetsDashboard = {
		unbilled_minutes: number;
		unbilled_estimate: number;
	};
</script>

<script lang="ts">
	import Money from '../../components/Money.svelte';
	import { minutes } from '../../format';

	let { data }: { data: TimesheetsDashboard } = $props();
</script>

<section class="card card-border bg-base-100 p-4 shadow-sm">
	<h2 class="mb-3 text-lg font-bold">Timesheets</h2>
	<dl class="m-0">
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Unbilled time</dt>
			<dd class="m-0 font-semibold tabular-nums">{minutes(data.unbilled_minutes)}</dd>
		</div>
		<div class="flex justify-between gap-4 border-t border-base-300 py-2 first:border-t-0">
			<dt class="text-base-content/70">Unbilled estimate</dt>
			<dd class="m-0 font-semibold tabular-nums"><Money cents={data.unbilled_estimate} /></dd>
		</div>
	</dl>
</section>
