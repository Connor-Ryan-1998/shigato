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

	$effect(() => {
		pnlParams;
		loadPnl();
	});
	$effect(() => {
		basParams;
		loadBas();
	});
</script>

<PageHeader title="Reports" />

<section aria-labelledby="pnl-heading">
	<h2 id="pnl-heading">Profit and loss</h2>
	<div class="controls">
		<Field label="From" id="pnl-from"><input id="pnl-from" type="date" bind:value={from} /></Field>
		<Field label="To" id="pnl-to"><input id="pnl-to" type="date" bind:value={to} /></Field>
		<Field label="Group by" id="pnl-group">
			<select id="pnl-group" bind:value={groupBy}>
				<option value="org">Organisation</option>
				<option value="property">Property</option>
				<option value="project">Project</option>
			</select>
		</Field>
	</div>
	<p class="exports">
		<a href={`${base}/pnl.csv?${pnlParams}`} download>P&amp;L CSV</a>
		<a href={`${base}/transactions.csv?${txParams}`} download>Transactions CSV</a>
	</p>
	{#if pnlError}
		<p class="error" role="alert">{pnlError}</p>
	{:else if pnl && pnl.length === 0}
		<p>No income or expenses in this range.</p>
	{:else if pnl}
		{#each pnl as g (g.key)}
			<table>
				<caption>{g.key}</caption>
				<tbody>
					{#each Object.entries(g.income) as [cat, cents] (cat)}
						<tr><th scope="row">{cat}</th><td>{money(cents)}</td></tr>
					{/each}
					<tr class="total"><th scope="row">Total income</th><td>{money(g.total_income)}</td></tr>
					{#each Object.entries(g.expenses) as [cat, cents] (cat)}
						<tr><th scope="row">{cat}</th><td>{money(cents)}</td></tr>
					{/each}
					<tr class="total"><th scope="row">Total expenses</th><td>{money(g.total_expenses)}</td></tr>
					<tr class="total net"><th scope="row">Net</th><td>{money(g.net)}</td></tr>
				</tbody>
			</table>
		{/each}
	{:else}
		<p>Loading…</p>
	{/if}
</section>

<section aria-labelledby="bas-heading">
	<h2 id="bas-heading">BAS summary</h2>
	<div class="controls">
		<Field label="Financial year ending" id="bas-year">
			<input id="bas-year" type="number" min="2000" max="2100" bind:value={basYear} />
		</Field>
		<Field label="Quarter" id="bas-quarter">
			<select id="bas-quarter" bind:value={basQuarter}>
				<option value={1}>Q1 (Jul–Sep)</option>
				<option value={2}>Q2 (Oct–Dec)</option>
				<option value={3}>Q3 (Jan–Mar)</option>
				<option value={4}>Q4 (Apr–Jun)</option>
			</select>
		</Field>
	</div>
	<p class="exports"><a href={`${base}/bas.csv?${basParams}`} download>BAS CSV</a></p>
	{#if basError}
		<p class="error" role="alert">{basError}</p>
	{:else if bas}
		<table>
			<caption>{period}</caption>
			<tbody>
				{#each BAS_ROWS as [k, label] (k)}
					<tr><th scope="row">{label}</th><td>{money(bas.figures_cents[k] ?? 0)}</td></tr>
				{/each}
			</tbody>
		</table>
	{:else}
		<p>Loading…</p>
	{/if}
</section>

<style>
	section {
		margin-bottom: var(--space-8, 2rem);
	}
	.controls {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-4, 1rem);
	}
	.exports {
		display: flex;
		gap: var(--space-4, 1rem);
	}
	table {
		width: 100%;
		max-width: 36rem;
		border-collapse: collapse;
		margin-bottom: var(--space-6, 1.5rem);
	}
	caption {
		text-align: left;
		font-weight: 600;
		padding-bottom: var(--space-2, 0.5rem);
	}
	th,
	td {
		padding: var(--space-2, 0.5rem) 0;
		border-bottom: 1px solid var(--border, #ddd);
		font-weight: normal;
		text-align: left;
	}
	td {
		text-align: right;
		font-variant-numeric: tabular-nums;
	}
	.total th,
	.total td {
		font-weight: 600;
	}
	.error {
		color: var(--danger, #b00020);
	}
</style>
