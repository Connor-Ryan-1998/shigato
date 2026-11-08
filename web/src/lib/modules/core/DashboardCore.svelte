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

	// org is the current org's slug: links must be absolute, since a relative
	// href resolves against /<org> to a path without the org in it.
	let { core, org }: { core: CoreDashboard; org: string } = $props();
</script>

<div class="mb-6 grid grid-cols-[repeat(auto-fit,minmax(14rem,1fr))] gap-4">
	<a class="card card-border bg-base-100 p-4 shadow-sm hover:bg-base-200" href={`/${org}/invoices?status=open`}>
		<p class="m-0 mb-2 text-sm font-semibold text-base-content/70">Open invoices</p>
		<p class="m-0 text-3xl font-bold">{core.open_count}</p>
		<p class="m-0 mt-1 text-sm text-base-content/70"><Money cents={core.open_outstanding} /> outstanding</p>
	</a>

	<!-- Styled as a warning, but "Overdue" is also shown as text, so the
	     status never depends on colour alone. -->
	<a class="card card-border bg-base-100 p-4 shadow-sm border-warning hover:bg-base-200" href={`/${org}/invoices?status=overdue`}>
		<p class="m-0 mb-2 text-sm font-semibold text-warning">Overdue</p>
		<p class="m-0 text-3xl font-bold">{core.overdue_count}</p>
		<p class="m-0 mt-1 text-sm text-base-content/70"><Money cents={core.overdue_outstanding} /> outstanding</p>
	</a>

	<div class="card card-border bg-base-100 p-4 shadow-sm">
		<p class="m-0 mb-2 text-sm font-semibold text-base-content/70">Drafts</p>
		<p class="m-0 text-3xl font-bold">{core.draft_count}</p>
	</div>
</div>
