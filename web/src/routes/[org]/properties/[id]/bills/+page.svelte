<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../../lib/api';
	import { date, money, todayLocal } from '../../../../../lib/format';
	import { session } from '../../../../../lib/session.svelte';
	import FilterBar from '../../../../../lib/components/FilterBar.svelte';
	import Field from '../../../../../lib/components/Field.svelte';
	import LoadState from '../../../../../lib/components/LoadState.svelte';
	import StatusBadge from '../../../../../lib/components/StatusBadge.svelte';
	import { fyFromURL, fyLabel, fyOptions, currentFY } from '../../../../../lib/modules/property/fy';

	type Line = {
		expense_id: number; line_id: number; spent_on: string; supplier: string; description: string;
		category: string; capital: boolean; incl_gst: number; gst: number; ex_gst: number;
		receipt_file_id: number | null; receipt_name: string;
	};
	type Bills = { fy: number; lines: Line[]; totals: Record<string, number> };

	const org = $derived(page.params.org ?? '');
	const id = $derived(page.params.id ?? '');
	const member = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(member?.perms.includes('property_read') ?? false);
	const canReceipts = $derived(member?.perms.includes('reports_read') ?? false);
	const canAdd = $derived(session.modules.includes('reconciliation') && (member?.perms.includes('recon_write') ?? false));
	const today = todayLocal();
	const fy = $derived(fyFromURL(page.url, today));
	const scope = $derived(`${org}/${id}/${fy}`);
	const options = fyOptions(currentFY(today));

	let bills = $state<Bills | null>(null);
	let loaded = $state('');
	let error = $state('');
	let seq = 0;

	async function load() {
		const token = ++seq;
		const s = scope;
		error = '';
		try {
			const result = await api<Bills>('GET', `/api/v1/orgs/${org}/properties/${id}/bills?fy=${fy}`);
			if (token !== seq || s !== scope || !canRead) return;
			bills = result;
			loaded = s;
		} catch (e) {
			if (token === seq && s === scope) error = e instanceof ApiError ? e.message : 'Could not load bills.';
		}
	}
	$effect(() => {
		scope; canRead;
		untrack(() => { loaded = ''; bills = null; if (canRead) load(); });
		return () => { seq++; };
	});

	function selectFY(value: string) {
		goto(`?fy=${value}`, { replaceState: true });
	}
	const totals = $derived(Object.entries(bills?.totals ?? {}).sort(([a], [b]) => a.localeCompare(b)));
	const total = $derived(totals.reduce((sum, [, v]) => sum + v, 0));
</script>

{#if !canRead}
	<p role="alert">You do not have permission to view properties.</p>
{:else}
	<div class="mb-4 flex flex-wrap items-end gap-3">
		<FilterBar label="Bill filters">
			<Field label="Financial year" id="bills-fy">
				<select class="select" id="bills-fy" value={String(fy)} onchange={(e) => selectFY(e.currentTarget.value)}>
					{#each options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
				</select>
			</Field>
		</FilterBar>
		<div class="ml-auto flex gap-2">
			<a class="btn btn-outline" href={`/${org}/reports/rental?kind=accountant&fy=${fy}&property_id=${id}`}>Tax report</a>
			{#if canAdd}<a class="btn btn-primary" href={`/${org}/expenses/new?property_id=${id}`}>Add bill</a>{/if}
		</div>
	</div>
	<p class="mb-4 text-base-content/70">Bills are expenses tagged to this property. Record what you paid; capital purchases are claimed through the Depreciation tab.</p>
	<LoadState loading={loaded !== scope} {error} empty={!!bills && bills.lines.length === 0} loadingLabel="Loading bills…" emptyTitle={`No bills recorded for ${fyLabel(fy)}.`} onretry={load}>
		{#if bills}
			<div class="grid gap-6 lg:grid-cols-[1fr_18rem]">
				<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
					<table class="table">
						<caption class="sr-only">Bills {fyLabel(fy)}</caption>
						<thead><tr><th>Date</th><th>Supplier</th><th>Description</th><th>Category</th><th class="text-right">Ex GST</th><th class="text-right">GST</th><th class="text-right">Total</th><th>Receipt</th><th><span class="sr-only">Expense</span></th></tr></thead>
						<tbody>
							{#each bills.lines as l (l.line_id)}
								<tr>
									<td>{date(l.spent_on)}</td>
									<td>{l.supplier || '—'}</td>
									<td>{l.description}</td>
									<td>{l.category}{#if l.capital} <StatusBadge status="Capital" tone="info" />{/if}</td>
									<td class="text-right tabular-nums">{money(l.ex_gst)}</td>
									<td class="text-right tabular-nums">{money(l.gst)}</td>
									<td class="text-right tabular-nums">{money(l.incl_gst)}</td>
									<td>{#if l.receipt_file_id && canReceipts}<a class="link link-primary" href={`/api/v1/orgs/${org}/files/${l.receipt_file_id}`}>{l.receipt_name || 'Receipt'}</a>{:else}—{/if}</td>
									<td><a class="link" href={`/${org}/expenses/${l.expense_id}`} aria-label={`View expense ${l.expense_id}`}>View</a></td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
				<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100 self-start">
					<table class="table">
						<caption class="sr-only">Totals by category</caption>
						<thead><tr><th>Category</th><th class="text-right">Ex GST</th></tr></thead>
						<tbody>
							{#each totals as [category, amount] (category)}
								<tr><td>{category}</td><td class="text-right tabular-nums">{money(amount)}</td></tr>
							{/each}
						</tbody>
						<tfoot><tr><th>Total</th><th class="text-right tabular-nums">{money(total)}</th></tr></tfoot>
					</table>
				</div>
			</div>
		{/if}
	</LoadState>
{/if}
