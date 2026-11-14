<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../../lib/api';
	import { date, money, todayLocal } from '../../../../../lib/format';
	import { session } from '../../../../../lib/session.svelte';
	import FilterBar from '../../../../../lib/components/FilterBar.svelte';
	import Field from '../../../../../lib/components/Field.svelte';
	import Button from '../../../../../lib/components/Button.svelte';
	import LoadState from '../../../../../lib/components/LoadState.svelte';
	import DepreciationItemDialog from '../../../../../lib/modules/property/DepreciationItemDialog.svelte';
	import { currentFY, fyFromURL, fyLabel, fyOptions } from '../../../../../lib/modules/property/fy';
	import { DEPRECIATION_TYPES, type DepreciationItem } from '../../../../../lib/modules/property/types';

	const org = $derived(page.params.org ?? '');
	const id = $derived(page.params.id ?? '');
	const scope = $derived(`${org}/${id}`);
	const member = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(member?.perms.includes('property_read') ?? false);
	const canWrite = $derived(member?.perms.includes('property_write') ?? false);
	const today = todayLocal();
	const thisFY = currentFY(today);
	const fy = $derived(fyFromURL(page.url, today));
	const options = fyOptions(thisFY, 10);
	const base = $derived(`/api/v1/orgs/${org}/properties/${id}`);

	let items = $state<DepreciationItem[]>([]);
	let loaded = $state('');
	let error = $state('');
	let dialogOpen = $state(false);
	let editing = $state<DepreciationItem | null>(null);
	let seq = 0;

	async function load() {
		const token = ++seq;
		const s = scope;
		error = '';
		try {
			const result = await api<DepreciationItem[]>('GET', `${base}/depreciation`);
			if (token !== seq || s !== scope || !canRead) return;
			items = result;
			loaded = s;
		} catch (e) {
			if (token === seq && s === scope) error = e instanceof ApiError ? e.message : 'Could not load depreciation.';
		}
	}
	$effect(() => {
		scope; canRead;
		untrack(() => { loaded = ''; items = []; dialogOpen = false; if (canRead) load(); });
		return () => { seq++; };
	});

	const amountFor = (item: DepreciationItem) => item.amounts.find((a) => a.fy === fy)?.amount ?? 0;
	const byType = $derived(DEPRECIATION_TYPES.map((t) => ({
		...t, total: items.filter((i) => i.type === t.value).reduce((sum, i) => sum + amountFor(i), 0)
	})));
	const total = $derived(byType.reduce((sum, t) => sum + t.total, 0));
	const typeLabel = (t: string) => DEPRECIATION_TYPES.find((x) => x.value === t)?.label ?? t;
</script>

{#if !canRead}
	<p role="alert">You do not have permission to view properties.</p>
{:else}
	<div class="mb-4 flex flex-wrap items-end gap-3">
		<FilterBar label="Depreciation filters">
			<Field label="Financial year" id="dep-filter-fy">
				<select class="select" id="dep-filter-fy" value={String(fy)} onchange={(e) => goto(`?fy=${e.currentTarget.value}`, { replaceState: true })}>
					{#each options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
				</select>
			</Field>
		</FilterBar>
		<div class="ml-auto flex gap-2">
			<a class="btn btn-outline" href={`/${org}/reports/rental?kind=item21&fy=${fy}&property_id=${id}`}>Item 21 report</a>
			{#if canWrite}<Button disabled={loaded !== scope} onclick={() => { editing = null; dialogOpen = true; }}>New item</Button>{/if}
		</div>
	</div>
	<p class="mb-4 text-base-content/70">Record each item's yearly deductions as your quantity surveyor's report (or your accountant) states them. Capital works covers the building and structural work; plant & equipment covers removable assets.</p>
	<LoadState loading={loaded !== scope} {error} empty={items.length === 0} loadingLabel="Loading depreciation…" emptyTitle="No depreciation items yet." onretry={load}>
		<section class="mb-6 grid gap-3 sm:grid-cols-3" aria-label={`${fyLabel(fy)} depreciation`}>
			{#each byType as t (t.value)}
				<div class="card card-border bg-base-100 p-4">
					<p class="text-sm text-base-content/70">{t.label}</p>
					<p class="text-xl font-semibold tabular-nums">{money(t.total)}</p>
				</div>
			{/each}
			<div class="card card-border bg-base-100 p-4">
				<p class="text-sm text-base-content/70">{fyLabel(fy)} total</p>
				<p class="total text-xl font-semibold tabular-nums">{money(total)}</p>
			</div>
		</section>
		<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
			<table class="table">
				<caption class="sr-only">Depreciation items</caption>
				<thead><tr><th>Item</th><th>Type</th><th>Installed</th><th class="text-right">Cost</th><th class="text-right">{fyLabel(fy)}</th><th>Documents</th><th><span class="sr-only">Actions</span></th></tr></thead>
				<tbody>
					{#each items as item (item.id)}
						<tr>
							<td>{item.name}{#if item.notes}<p class="text-sm text-base-content/70 whitespace-pre-wrap">{item.notes}</p>{/if}</td>
							<td>{typeLabel(item.type)}</td>
							<td>{date(item.installed_on)}</td>
							<td class="text-right tabular-nums">{item.cost === null ? '—' : money(item.cost)}</td>
							<td class="text-right tabular-nums">{money(amountFor(item))}</td>
							<td>
								{#each item.documents as d (d.id)}<a class="link link-primary block" href={`${base}/files/${d.id}`}>{d.original_name}</a>{:else}—{/each}
							</td>
							<td>{#if canWrite}<Button variant="secondary" size="sm" onclick={() => { editing = item; dialogOpen = true; }}>Edit {item.name}</Button>{/if}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</LoadState>
	{#if canWrite}
		<DepreciationItemDialog bind:open={dialogOpen} {org} propertyId={Number(id)} item={editing} defaultFY={fy} onsaved={load} />
	{/if}
{/if}
