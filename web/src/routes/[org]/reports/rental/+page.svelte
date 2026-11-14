<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import { date, money } from '../../../../lib/format';
	import { session } from '../../../../lib/session.svelte';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import LoadState from '../../../../lib/components/LoadState.svelte';
	import { fyLabel } from '../../../../lib/modules/property/fy';
	import { DEPRECIATION_TYPES, ITEM21_LABELS } from '../../../../lib/modules/property/types';

	type Line = {
		expense_id: number; line_id: number; spent_on: string; supplier: string; description: string; category: string;
		item21_label: string | null; incl_gst: number; gst: number; ex_gst: number; receipt_name: string;
	};
	type Summary = {
		property_id: number; name: string; gross_rent: number; other_income: number;
		deductions: Record<string, number>; total_deductions: number; net: number; warnings: string[];
		income?: { paid_on: string; invoice_id: number; invoice_number: number | null; category: string; incl_gst: number; gst: number; ex_gst: number }[];
		expenses?: Line[]; capital?: Line[];
		depreciation?: { item_id: number; name: string; type: string; amount: number }[];
	};
	type Report = { fy: number; properties: Summary[] };

	const org = $derived(page.params.org ?? '');
	const params = $derived(page.url?.searchParams);
	const kind = $derived(params?.get('kind') === 'accountant' ? 'accountant' : 'item21');
	const fy = $derived(Number(params?.get('fy')) || 0);
	const propertyId = $derived(params?.get('property_id') ?? '');
	const query = $derived(`fy=${fy}${propertyId ? `&property_id=${propertyId}` : ''}`);
	const scope = $derived(`${org}/${kind}/${query}`);
	const canRead = $derived(session.currentOrg?.slug === org && (session.currentOrg?.perms.includes('reports_read') ?? false));
	const title = $derived(`${kind === 'item21' ? 'Item 21 rental schedule' : 'Accountant report'} ${fy ? fyLabel(fy) : ''}`.trim());

	let report = $state<Report | null>(null);
	let loaded = $state('');
	let error = $state('');
	let seq = 0;

	async function load() {
		const token = ++seq;
		const s = scope;
		error = '';
		try {
			const result = await api<Report>('GET', `/api/v1/orgs/${org}/rental-reports/${kind}?${query}`);
			if (token !== seq || s !== scope) return;
			report = result;
			loaded = s;
		} catch (e) {
			if (token === seq && s === scope) error = e instanceof ApiError ? e.message : 'Could not load the report.';
		}
	}
	$effect(() => {
		scope; canRead;
		untrack(() => { loaded = ''; report = null; if (canRead) load(); });
		return () => { seq++; };
	});

	const labelOrder = ITEM21_LABELS.map((l) => l.value);
	const deductionRows = (s: Summary) =>
		labelOrder.filter((l) => l in s.deductions).map((l) => [ITEM21_LABELS.find((x) => x.value === l)!.label, s.deductions[l]] as const);
	const typeLabel = (t: string) => DEPRECIATION_TYPES.find((x) => x.value === t)?.label ?? t;
	// Name the property only when the report covers several.
	const warnings = $derived(report?.properties.flatMap((p) => p.warnings.map((w) => (report!.properties.length > 1 ? `${p.name}: ${w}` : w))) ?? []);
</script>

<PageHeader {title} back={{ href: `/${org}/reports`, label: 'Back to reports' }}>
	{#snippet actions()}
		<div class="flex gap-2 print:hidden">
			<a class="btn btn-outline" href={`/api/v1/orgs/${org}/rental-reports/${kind}?${query}&format=csv`} download>Download CSV</a>
			<Button onclick={() => window.print()}>Print</Button>
		</div>
	{/snippet}
</PageHeader>

{#snippet itemTable(s: Summary, caption: string)}
	<table class="table mb-6 max-w-2xl [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold [&_tbody_th]:font-normal [&_td]:text-right [&_td]:tabular-nums">
		<caption>{caption}</caption>
		<tbody>
			<tr><th scope="row">Gross rent</th><td>{money(s.gross_rent)}</td></tr>
			<tr><th scope="row">Other rental related income</th><td>{money(s.other_income)}</td></tr>
			{#each deductionRows(s) as [label, cents] (label)}
				<tr><th scope="row">{label}</th><td>{money(cents)}</td></tr>
			{/each}
			<tr class="font-semibold [&_th]:font-semibold"><th scope="row">Total deductions</th><td>{money(s.total_deductions)}</td></tr>
			<tr class="font-semibold [&_th]:font-semibold"><th scope="row">Net rent</th><td>{money(s.net)}</td></tr>
		</tbody>
	</table>
{/snippet}

{#snippet lineTable(lines: Line[], caption: string)}
	<div class="mb-6 overflow-x-auto">
		<table class="table [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold">
			<caption>{caption}</caption>
			<thead><tr><th>Date</th><th>Supplier</th><th>Description</th><th>Category</th><th>Item 21</th><th class="text-right">Ex GST</th><th class="text-right">GST</th><th class="text-right">Total</th><th>Receipt</th></tr></thead>
			<tbody>
				{#each lines as l (l.line_id)}
					<tr>
						<td>{date(l.spent_on)}</td><td>{l.supplier || '—'}</td><td>{l.description}</td><td>{l.category}</td>
						<td>{l.item21_label ? ITEM21_LABELS.find((x) => x.value === l.item21_label)?.label : '—'}</td>
						<td class="text-right tabular-nums">{money(l.ex_gst)}</td><td class="text-right tabular-nums">{money(l.gst)}</td>
						<td class="text-right tabular-nums">{money(l.incl_gst)}</td><td>{l.receipt_name || '—'}</td>
					</tr>
				{:else}
					<tr><td colspan="9">None.</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#if !canRead}
	<p role="alert">You do not have permission to view reports.</p>
{:else}
	<LoadState loading={loaded !== scope} {error} empty={!!report && report.properties.length === 0} loadingLabel="Loading report…" emptyTitle="No properties to report." onretry={load}>
		{#if report}
			{#each warnings as w (w)}<p role="status" class="alert alert-warning alert-soft mb-3">{w}</p>{/each}
			{#each report.properties as s (s.property_id)}
				<section class="mb-10 break-inside-avoid" aria-label={s.name}>
					{#if kind === 'item21'}
						{@render itemTable(s, s.name)}
					{:else}
						<h2 class="mb-3 text-xl font-bold">{s.name}</h2>
						{@render itemTable(s, `${s.name} summary`)}
						<div class="mb-6 overflow-x-auto">
							<table class="table [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold">
								<caption>{s.name} income</caption>
								<thead><tr><th>Received</th><th>Invoice</th><th>Category</th><th class="text-right">Ex GST</th><th class="text-right">GST</th><th class="text-right">Total</th></tr></thead>
								<tbody>
									{#each s.income ?? [] as r, i (i)}
										<tr><td>{date(r.paid_on)}</td><td>{r.invoice_number ? `Invoice #${r.invoice_number}` : `Invoice ${r.invoice_id}`}</td><td>{r.category}</td>
											<td class="text-right tabular-nums">{money(r.ex_gst)}</td><td class="text-right tabular-nums">{money(r.gst)}</td><td class="text-right tabular-nums">{money(r.incl_gst)}</td></tr>
									{:else}
										<tr><td colspan="6">None.</td></tr>
									{/each}
								</tbody>
							</table>
						</div>
						{@render lineTable(s.expenses ?? [], `${s.name} expenses`)}
						{@render lineTable(s.capital ?? [], `${s.name} capital purchases`)}
						<div class="mb-6 overflow-x-auto">
							<table class="table [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold">
								<caption>{s.name} depreciation</caption>
								<thead><tr><th>Item</th><th>Type</th><th class="text-right">{fyLabel(fy)}</th></tr></thead>
								<tbody>
									{#each s.depreciation ?? [] as d (d.item_id)}
										<tr><td>{d.name}</td><td>{typeLabel(d.type)}</td><td class="text-right tabular-nums">{money(d.amount)}</td></tr>
									{:else}
										<tr><td colspan="3">None.</td></tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}
				</section>
			{/each}
		{/if}
	</LoadState>
{/if}
