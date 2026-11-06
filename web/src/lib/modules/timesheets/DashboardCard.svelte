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

<section class="card">
	<h2>Timesheets</h2>
	<dl>
		<div class="row">
			<dt>Unbilled time</dt>
			<dd>{minutes(data.unbilled_minutes)}</dd>
		</div>
		<div class="row">
			<dt>Unbilled estimate</dt>
			<dd><Money cents={data.unbilled_estimate} /></dd>
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
