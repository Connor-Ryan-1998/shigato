<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import { date, money, parseCents, todayLocal } from '../../../../lib/format';
	import { session } from '../../../../lib/session.svelte';
	import { useUnsavedChanges } from '../../../../lib/forms/unsaved.svelte';
	import Field from '../../../../lib/components/Field.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import FormActions from '../../../../lib/components/FormActions.svelte';
	import LoadState from '../../../../lib/components/LoadState.svelte';
	import StatusBadge from '../../../../lib/components/StatusBadge.svelte';
	import InsuranceDialog from '../../../../lib/modules/property/InsuranceDialog.svelte';
	import { propertyHeader } from '../../../../lib/modules/property/header.svelte';
	import { centsInput, INSURANCE_TYPES, type InsurancePolicy, type PropertyDetail } from '../../../../lib/modules/property/types';

	type Form = {
		name: string; address: string; kind: string; residential: boolean;
		contract_date: string; settlement_date: string; purchase_price: string; stamp_duty: string;
		purchase_legal_costs: string; year_built: string; lot_plan: string; land_area_m2: string;
		council_account_ref: string; water_account_ref: string;
	};

	const org = $derived(page.params.org ?? '');
	const id = $derived(page.params.id ?? '');
	const scope = $derived(`${org}/${id}`);
	const member = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(member?.perms.includes('property_read') ?? false);
	const canWrite = $derived(member?.perms.includes('property_write') ?? false);
	const base = $derived(`/api/v1/orgs/${org}/properties/${id}`);

	let property = $state<PropertyDetail | null>(null);
	let policies = $state<InsurancePolicy[]>([]);
	let form = $state<Form | null>(null);
	let snapshot = $state('');
	let loaded = $state('');
	let error = $state('');
	let saveError = $state('');
	let success = $state('');
	let saving = $state(false);
	let policyOpen = $state(false);
	let editingPolicy = $state<InsurancePolicy | null>(null);
	let seq = 0;

	const dirty = $derived(form !== null && loaded === scope && JSON.stringify(form) !== snapshot);
	const { allowLeave } = useUnsavedChanges(() => dirty, () => saving);

	function toForm(p: PropertyDetail): Form {
		return {
			name: p.name, address: p.address ?? '', kind: p.kind ?? '', residential: p.residential,
			contract_date: p.contract_date ?? '', settlement_date: p.settlement_date ?? '',
			purchase_price: centsInput(p.purchase_price), stamp_duty: centsInput(p.stamp_duty),
			purchase_legal_costs: centsInput(p.purchase_legal_costs),
			year_built: p.year_built === null ? '' : String(p.year_built), lot_plan: p.lot_plan,
			land_area_m2: p.land_area_m2 === null ? '' : String(p.land_area_m2),
			council_account_ref: p.council_account_ref, water_account_ref: p.water_account_ref
		};
	}
	function accept(p: PropertyDetail) {
		property = p;
		form = toForm(p);
		snapshot = JSON.stringify(form);
		propertyHeader.key = scope;
		propertyHeader.name = p.name;
	}

	async function load() {
		const token = ++seq;
		const s = scope;
		error = '';
		try {
			const [p, ins] = await Promise.all([api<PropertyDetail>('GET', base), api<InsurancePolicy[]>('GET', `${base}/insurance`)]);
			if (token !== seq || s !== scope || !canRead) return;
			accept(p);
			policies = ins;
			loaded = s;
		} catch (e) {
			if (token === seq && s === scope) error = e instanceof ApiError ? e.message : 'Could not load the property.';
		}
	}
	async function reloadPolicies() {
		const token = seq;
		const s = scope;
		try {
			const ins = await api<InsurancePolicy[]>('GET', `${base}/insurance`);
			if (token === seq && s === scope) policies = ins;
		} catch (e) {
			if (token === seq && s === scope) error = e instanceof ApiError ? e.message : 'Could not refresh insurance.';
		}
	}
	$effect(() => {
		scope; canRead;
		untrack(() => {
			loaded = ''; property = null; form = null; policies = []; saving = false; saveError = ''; success = '';
			if (canRead) load();
		});
		return () => { seq++; };
	});

	function cancel() {
		if (!snapshot || !allowLeave()) return;
		form = JSON.parse(snapshot);
		saveError = ''; success = '';
	}

	// optionalCents: '' is null; an unparseable or negative value is undefined.
	function optionalCents(v: string): number | null | undefined {
		if (v.trim() === '') return null;
		const c = parseCents(v);
		return c === null || c < 0 ? undefined : c;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!form || saving || !canWrite || loaded !== scope) return;
		const amounts = {
			purchase_price: optionalCents(form.purchase_price), stamp_duty: optionalCents(form.stamp_duty),
			purchase_legal_costs: optionalCents(form.purchase_legal_costs)
		};
		if (Object.values(amounts).some((v) => v === undefined)) {
			saveError = 'Enter amounts in dollars, with up to two decimal places.';
			return;
		}
		const year = form.year_built.trim() === '' ? null : Number(form.year_built);
		const area = form.land_area_m2.trim() === '' ? null : Number(form.land_area_m2);
		if ((year !== null && !Number.isInteger(year)) || (area !== null && (!Number.isFinite(area) || area < 0))) {
			saveError = 'Year built must be a whole year and land area a positive number.';
			return;
		}
		const body = {
			name: form.name, address: form.address, kind: form.kind, residential: form.residential,
			contract_date: form.contract_date || null, settlement_date: form.settlement_date || null,
			...amounts, year_built: year, lot_plan: form.lot_plan, land_area_m2: area,
			council_account_ref: form.council_account_ref, water_account_ref: form.water_account_ref
		};
		const token = seq;
		const s = scope;
		saving = true; saveError = ''; success = '';
		try {
			const saved = await api<PropertyDetail>('PUT', base, body);
			if (token !== seq || s !== scope) return;
			accept(saved);
			success = 'Details saved.';
		} catch (err) {
			if (token === seq && s === scope) saveError = err instanceof ApiError ? err.message : 'Could not save details.';
		} finally {
			if (token === seq) saving = false;
		}
	}

	const mapsHref = $derived(property?.address ? `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(property.address)}` : '');

	function daysUntil(iso: string): number {
		const [y, m, d] = iso.split('-').map(Number);
		const [ty, tm, td] = todayLocal().split('-').map(Number);
		return Math.round((Date.UTC(y, m - 1, d) - Date.UTC(ty, tm - 1, td)) / 86_400_000);
	}
	const renewalBadge = $derived.by(() => {
		const latest = policies[0];
		if (!latest) return null;
		const n = daysUntil(latest.renewal_date);
		if (n < 0) return { text: 'Expired', tone: 'danger' as const };
		if (n <= 30) return { text: `Renews in ${n} day${n === 1 ? '' : 's'}`, tone: 'warning' as const };
		return null;
	});
	const typeLabel = (t: string) => INSURANCE_TYPES.find((x) => x.value === t)?.label ?? t;
	const optMoney = (c: number | null) => (c === null ? '—' : money(c));
</script>

{#if !canRead}
	<p role="alert">You do not have permission to view properties.</p>
{:else}
	<LoadState loading={loaded !== scope} {error} empty={false} loadingLabel="Loading property…" onretry={load}>
		{#if form && property}
			<div class="grid gap-6 lg:grid-cols-[1fr_20rem]">
				<form onsubmit={save} class="grid gap-4">
					{#if saveError}<p role="alert" class="alert alert-error alert-soft">{saveError}</p>{/if}
					{#if success}<p role="status" class="alert alert-success alert-soft">{success}</p>{/if}
					<fieldset disabled={saving || !canWrite} class="fieldset grid gap-3 rounded-box border border-base-300 bg-base-100 p-4 sm:grid-cols-2">
						<legend class="fieldset-legend">Property</legend>
						<Field label="Name" id="prop-name"><input class="input w-full" id="prop-name" bind:value={form.name} required /></Field>
						<Field label="Kind" id="prop-kind"><input class="input w-full" id="prop-kind" bind:value={form.kind} placeholder="house, unit…" /></Field>
						<div class="sm:col-span-2">
							<Field label="Address" id="prop-address"><input class="input w-full" id="prop-address" bind:value={form.address} /></Field>
							{#if mapsHref}<a class="link link-primary text-sm" href={mapsHref} target="_blank" rel="noopener noreferrer">Open in Maps</a>{/if}
						</div>
						<label class="label"><input class="checkbox" type="checkbox" bind:checked={form.residential} /> Residential</label>
					</fieldset>
					<fieldset disabled={saving || !canWrite} class="fieldset grid gap-3 rounded-box border border-base-300 bg-base-100 p-4 sm:grid-cols-2">
						<legend class="fieldset-legend">Purchase</legend>
						<Field label="Contract date" id="prop-contract"><input class="input w-full" id="prop-contract" type="date" bind:value={form.contract_date} /></Field>
						<Field label="Settlement date" id="prop-settlement"><input class="input w-full" id="prop-settlement" type="date" bind:value={form.settlement_date} /></Field>
						<Field label="Purchase price ($)" id="prop-price"><input class="input w-full" id="prop-price" inputmode="decimal" bind:value={form.purchase_price} /></Field>
						<Field label="Stamp duty ($)" id="prop-duty"><input class="input w-full" id="prop-duty" inputmode="decimal" bind:value={form.stamp_duty} /></Field>
						<Field label="Purchase legal costs ($)" id="prop-legal" hint="Stamp duty and legal costs form part of the capital gains cost base.">
							<input class="input w-full" id="prop-legal" inputmode="decimal" bind:value={form.purchase_legal_costs} />
						</Field>
					</fieldset>
					<fieldset disabled={saving || !canWrite} class="fieldset grid gap-3 rounded-box border border-base-300 bg-base-100 p-4 sm:grid-cols-2">
						<legend class="fieldset-legend">Build and reference</legend>
						<Field label="Year built" id="prop-year" hint="Residential buildings started after 15 Sept 1987 qualify for capital works deductions.">
							<input class="input w-full" id="prop-year" inputmode="numeric" bind:value={form.year_built} />
						</Field>
						<Field label="Land area (m²)" id="prop-area"><input class="input w-full" id="prop-area" inputmode="decimal" bind:value={form.land_area_m2} /></Field>
						<Field label="Lot/plan" id="prop-lot"><input class="input w-full" id="prop-lot" bind:value={form.lot_plan} /></Field>
						<Field label="Council rates account" id="prop-council"><input class="input w-full" id="prop-council" bind:value={form.council_account_ref} /></Field>
						<Field label="Water account" id="prop-water"><input class="input w-full" id="prop-water" bind:value={form.water_account_ref} /></Field>
					</fieldset>
					{#if canWrite}<FormActions busy={saving} submitLabel="Save details" oncancel={cancel} />{/if}
				</form>

				<div class="grid content-start gap-4">
					<section class="card card-border bg-base-100 p-4" aria-label="Current tenancy">
						<h2 class="mb-2 text-lg font-bold">Current tenancy</h2>
						{#if property.current_tenancy}
							{@const t = property.current_tenancy}
							<p>{t.primary_tenant_name}</p>
							<p class="tabular-nums">{money(t.rent)} {t.frequency}</p>
							<a class="link link-primary" href={`/${org}/leases/${t.lease_id}`}>View lease</a>
						{:else}
							<p class="text-base-content/70">No active lease.</p>
						{/if}
					</section>
				</div>
			</div>

			<section class="mt-6" aria-labelledby="insurance-heading">
				<div class="mb-3 flex flex-wrap items-center gap-3">
					<h2 id="insurance-heading" class="text-lg font-bold">Insurance</h2>
					{#if renewalBadge}<StatusBadge status={renewalBadge.text} tone={renewalBadge.tone} />{/if}
					{#if canWrite}<div class="ml-auto"><Button onclick={() => { editingPolicy = null; policyOpen = true; }}>Add policy</Button></div>{/if}
				</div>
				{#if policies.length === 0}
					<p class="text-base-content/70">No insurance policies recorded.</p>
				{:else}
					<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
						<table class="table">
							<thead><tr><th>Insurer</th><th>Type</th><th>Policy</th><th>Period</th><th class="text-right">Sum insured</th><th class="text-right">Premium</th><th class="text-right">Excess</th><th>Document</th><th><span class="sr-only">Actions</span></th></tr></thead>
							<tbody>
								{#each policies as p (p.id)}
									<tr>
										<td>{p.insurer}</td>
										<td>{typeLabel(p.type)}</td>
										<td>{p.policy_number || '—'}</td>
										<td>{date(p.start_date)} – {date(p.renewal_date)}</td>
										<td class="text-right tabular-nums">{optMoney(p.sum_insured)}</td>
										<td class="text-right tabular-nums">{optMoney(p.premium)}</td>
										<td class="text-right tabular-nums">{optMoney(p.excess)}</td>
										<td>{#if p.file_id}<a class="link link-primary" href={`${base}/files/${p.file_id}`}>{p.file_name}</a>{:else}—{/if}</td>
										<td>{#if canWrite}<Button variant="secondary" size="sm" onclick={() => { editingPolicy = p; policyOpen = true; }}>Edit</Button>{/if}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</section>
			{#if canWrite}
				<InsuranceDialog bind:open={policyOpen} {org} propertyId={property.id} policy={editingPolicy} onsaved={reloadPolicies} />
			{/if}
		{/if}
	</LoadState>
{/if}
