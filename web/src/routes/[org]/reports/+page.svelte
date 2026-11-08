<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { money, todayLocal } from '../../../lib/format';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Button from '../../../lib/components/Button.svelte';

	type PnLGroup = {
		key: string;
		income: Record<string, number>;
		expenses: Record<string, number>;
		total_income: number;
		total_expenses: number;
		net: number;
	};
	type BAS = { period?: string; figures_cents: Record<string, number> };
	type DetailRow = {
		payment_id?: number;
		invoice_number?: number;
		paid_on?: string;
		expense_id?: number;
		line_id?: number;
		spent_on?: string;
		category: string;
		gst_code: string;
		incl_gst: number;
		gst: number;
		ex_gst: number;
	};
	type BASDetail = { field: string; income?: DetailRow[]; expenses?: DetailRow[] };

	// Australian FY (1 July start). ponytail: assumes fy_start_month 7; read
	// the org setting if a non-AU org ever appears.
	const today = todayLocal();
	const [y, m] = today.split('-').map(Number);
	const fyEndYear = m >= 7 ? y + 1 : y;
	const currentQuarter = Math.floor(((m + 5) % 12) / 3) + 1;

	const BAS_ROWS: [string, string][] = [
		['g1', 'G1 Total sales'],
		['g2', 'G2 Export sales'],
		['g3', 'G3 Other GST-free sales'],
		['g4', 'G4 Input-taxed sales'],
		['g10', 'G10 Capital purchases'],
		['g11', 'G11 Non-capital purchases'],
		['1a', '1A GST on sales'],
		['1b', '1B GST on purchases']
	];

	let from = $state(`${fyEndYear - 1}-07-01`);
	let to = $state(today);
	let groupBy = $state('org');
	let basYear = $state(fyEndYear);
	let basQuarter = $state(currentQuarter);

	let pnl = $state<PnLGroup[] | null>(null);
	let pnlError = $state('');
	let bas = $state<BAS | null>(null);
	let basError = $state('');
	let pnlSeq = 0;
	let basSeq = 0;
	let detail = $state<BASDetail | null>(null);
	let detailError = $state('');
	let detailSeq = 0;

	const base = $derived(`/api/v1/orgs/${page.params.org}/reports`);
	const pnlParams = $derived(new URLSearchParams({ from, to, group_by: groupBy }).toString());
	const period = $derived(`FY${basYear}-Q${basQuarter}`);
	const basParams = $derived(new URLSearchParams({ period }).toString());
	const txParams = $derived(new URLSearchParams({ from, to }).toString());

	function message(e: unknown): string {
		return e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	async function loadPnl() {
		const seq = ++pnlSeq;
		pnlError = '';
		try {
			const res = await api<PnLGroup[]>('GET', `${base}/pnl?${pnlParams}`);
			if (seq === pnlSeq) pnl = res;
		} catch (e) {
			if (seq === pnlSeq) pnlError = message(e);
		}
	}

	async function loadBas() {
		const seq = ++basSeq;
		basError = '';
		try {
			const res = await api<BAS>('GET', `${base}/bas?${basParams}`);
			if (seq === basSeq) bas = res;
		} catch (e) {
			if (seq === basSeq) basError = message(e);
		}
	}

	async function loadDetail(field: string) {
		const seq = ++detailSeq;
		detailError = '';
		detail = null;
		try {
			const q = new URLSearchParams({ period, field }).toString();
			const res = await api<BASDetail>('GET', `${base}/bas/detail?${q}`);
			if (seq === detailSeq) detail = res;
		} catch (e) {
			if (seq === detailSeq) detailError = message(e);
		}
	}

	$effect(() => {
		pnlParams;
		loadPnl();
	});
	$effect(() => {
		basParams;
		detailSeq++;
		detail = null;
		detailError = '';
		loadBas();
	});
</script>

<PageHeader title="Reports" />

<section class="mb-8" aria-labelledby="pnl-heading">
	<h2 id="pnl-heading" class="mb-3 text-lg font-semibold">Profit and loss</h2>
	<div class="flex flex-wrap gap-4">
		<Field label="From" id="pnl-from"><input class="input" id="pnl-from" type="date" bind:value={from} /></Field>
		<Field label="To" id="pnl-to"><input class="input" id="pnl-to" type="date" bind:value={to} /></Field>
		<Field label="Group by" id="pnl-group">
			<select class="select" id="pnl-group" bind:value={groupBy}>
				<option value="org">Organisation</option>
				<option value="property">Property</option>
				<option value="project">Project</option>
			</select>
		</Field>
	</div>
	<p class="my-3 flex gap-4">
		<a href={`${base}/pnl.csv?${pnlParams}`} class="link link-primary" download>P&amp;L CSV</a>
		<a href={`${base}/transactions.csv?${txParams}`} class="link link-primary" download>Transactions CSV</a>
	</p>
	{#if pnlError}
		<p class="alert alert-error alert-soft" role="alert">{pnlError}</p>
	{:else if pnl && pnl.length === 0}
		<p>No income or expenses in this range.</p>
	{:else if pnl}
		{#each pnl as g (g.key)}
			<table class="table mb-6 max-w-xl [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold [&_tbody_th]:font-normal [&_td]:text-right [&_td]:tabular-nums">
				<caption>{g.key}</caption>
				<tbody>
					{#each Object.entries(g.income) as [cat, cents] (cat)}
						<tr><th scope="row">{cat}</th><td>{money(cents)}</td></tr>
					{/each}
					<tr class="font-semibold [&_th]:font-semibold"><th scope="row">Total income</th><td>{money(g.total_income)}</td></tr>
					{#each Object.entries(g.expenses) as [cat, cents] (cat)}
						<tr><th scope="row">{cat}</th><td>{money(cents)}</td></tr>
					{/each}
					<tr class="font-semibold [&_th]:font-semibold"><th scope="row">Total expenses</th><td>{money(g.total_expenses)}</td></tr>
					<tr class="font-semibold [&_th]:font-semibold"><th scope="row">Net</th><td>{money(g.net)}</td></tr>
				</tbody>
			</table>
		{/each}
	{:else}
		<p>Loading…</p>
	{/if}
</section>

<section class="mb-8" aria-labelledby="bas-heading">
	<h2 id="bas-heading" class="mb-3 text-lg font-semibold">BAS summary</h2>
	<div class="flex flex-wrap gap-4">
		<Field label="Financial year ending" id="bas-year">
			<input class="input" id="bas-year" type="number" min="2000" max="2100" bind:value={basYear} />
		</Field>
		<Field label="Quarter" id="bas-quarter">
			<select class="select" id="bas-quarter" bind:value={basQuarter}>
				<option value={1}>Q1 (Jul–Sep)</option>
				<option value={2}>Q2 (Oct–Dec)</option>
				<option value={3}>Q3 (Jan–Mar)</option>
				<option value={4}>Q4 (Apr–Jun)</option>
			</select>
		</Field>
	</div>
	<p class="my-3 flex gap-4"><a href={`${base}/bas.csv?${basParams}`} class="link link-primary" download>BAS CSV</a></p>
	{#if basError}
		<p class="alert alert-error alert-soft" role="alert">{basError}</p>
	{:else if bas}
		<table class="table mb-6 max-w-xl [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold [&_tbody_th]:font-normal [&_td]:text-right [&_td]:tabular-nums">
			<caption>{period}</caption>
			<tbody>
				{#each BAS_ROWS as [k, label] (k)}
					<tr>
						<th scope="row">
							{#if k === 'g2'}
								{label}
							{:else}
								<button type="button" class="link link-primary" onclick={() => loadDetail(k.toUpperCase())}>{label}</button>
							{/if}
						</th>
						<td>{money(bas.figures_cents[k] ?? 0)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
		{#if detailError}
			<p class="alert alert-error alert-soft" role="alert">{detailError}</p>
		{:else if detail}
			{@const rows = [...(detail.income ?? []), ...(detail.expenses ?? [])]}
			<div class="overflow-x-auto">
			<table class="table mb-6 [&_caption]:pb-2 [&_caption]:text-left [&_caption]:font-semibold [&_td]:tabular-nums">
				<caption>{detail.field} detail</caption>
				<thead>
					<tr><th>Date</th><th>Source</th><th>Category</th><th>GST code</th><th>Ex GST</th><th>GST</th><th>Incl GST</th></tr>
				</thead>
				<tbody>
					{#each rows as row, i (i)}
						<tr>
							<td>{row.paid_on ?? row.spent_on}</td>
							<td>
								{#if row.payment_id !== undefined}
									Invoice {row.invoice_number ?? '(draft)'}
								{:else}
									Expense {row.expense_id}
								{/if}
							</td>
							<td>{row.category}</td>
							<td>{row.gst_code}</td>
							<td>{money(row.ex_gst)}</td>
							<td>{money(row.gst)}</td>
							<td>{money(row.incl_gst)}</td>
						</tr>
					{:else}
						<tr><td colspan="7">No rows.</td></tr>
					{/each}
				</tbody>
			</table>
			</div>
		{/if}
	{:else}
		<p>Loading…</p>
	{/if}
</section>
