<script lang="ts" module>
	// Shape of the dashboard response's "core" section (GET /dashboard):
	// always present, regardless of which modules are enabled.
	export type CoreDashboard = {
		open_count: number;
		open_outstanding: number;
		overdue_count: number;
		overdue_outstanding: number;
		draft_count: number;
		// Every invoice in the org regardless of status - unlike the counts
		// above, which are each scoped to one status. Lets the dashboard page
		// tell "no invoices at all" apart from "fully settled" (open/overdue/
		// draft all 0, but some are paid/void).
		invoice_count: number;
	};
</script>

<script lang="ts">
	import Money from '../../components/Money.svelte';

	let { core }: { core: CoreDashboard } = $props();
</script>

<div class="tiles">
	<a class="tile" href="invoices?status=open">
		<p class="label">Open invoices</p>
		<p class="value">{core.open_count}</p>
		<p class="sub"><Money cents={core.open_outstanding} /> outstanding</p>
	</a>

	<!-- Styled as a warning, but "Overdue" is also shown as text, so the
	     status never depends on colour alone. -->
	<a class="tile tile-warning" href="invoices?status=overdue">
		<p class="label">Overdue</p>
		<p class="value">{core.overdue_count}</p>
		<p class="sub"><Money cents={core.overdue_outstanding} /> outstanding</p>
	</a>

	<div class="tile">
		<p class="label">Drafts</p>
		<p class="value">{core.draft_count}</p>
	</div>
</div>

<style>
	.tiles {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
		gap: var(--space-4);
		margin-bottom: var(--space-6);
	}
	.tile {
		display: block;
		padding: var(--space-4);
		border-radius: var(--radius-lg);
		background: var(--surface);
		border: 1px solid var(--border);
		text-decoration: none;
		color: inherit;
	}
	a.tile:hover {
		background: var(--surface-raised);
	}
	.tile-warning {
		border-color: var(--warning);
	}
	.tile-warning .label {
		color: var(--warning);
	}
	.label {
		margin: 0 0 var(--space-2);
		font-size: var(--text-sm);
		font-weight: 600;
		color: var(--text-muted);
	}
	.value {
		margin: 0;
		font-size: var(--text-2xl);
		font-weight: 700;
	}
	.sub {
		margin: var(--space-1) 0 0;
		color: var(--text-muted);
		font-size: var(--text-sm);
	}
</style>
