<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { money, todayLocal } from '../../../lib/format';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import Toast from '../../../lib/components/Toast.svelte';

	type Property = { id: number; name: string; address?: string; residential: boolean };
	type Lease = {
		id: number;
		property_id: number;
		start_date: string;
		termination_date?: string;
		frequency: string;
		rent_changes: { effective_date: string; amount: number }[];
		tenants: { contact_id: number; primary: boolean }[];
	};
	type Contact = { id: number; name: string; kind: string };

	const FREQUENCIES = [
		{ value: 'weekly', label: 'Weekly' },
		{ value: 'fortnightly', label: 'Fortnightly' },
		{ value: 'monthly', label: 'Monthly' }
	];

	const org = $derived(page.params.org);
	const base = $derived(`/api/v1/orgs/${org}`);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('property_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('property_write') ?? false);

	let properties = $state<Property[]>([]);
	let leases = $state<Lease[]>([]);
	let contacts = $state<Contact[]>([]);
	let error = $state('');
	let loadedOrg = $state('');
	let busy = $state(false);
	let loadSeq = 0;
	let actionSeq = 0;

	let propName = $state('');
	let propAddress = $state('');
	let propResidential = $state(true);

	let leaseProperty = $state('');
	let leaseTenant = $state('');
	let leaseStart = $state(todayLocal());
	let leaseFrequency = $state('weekly');
	let leaseRent = $state('');
	let leaseBond = $state('');
	let coTenants = $state<string[]>([]);
	let paidTo = $state('');
	let openingArrears = $state('');

	const contactName = $derived(new Map(contacts.map((c) => [c.id, c.name])));
	const propertyOptions = $derived(properties.map((p) => ({ value: String(p.id), label: p.name })));
	const tenantOptions = $derived(
		contacts.filter((c) => c.kind === 'tenant').map((c) => ({ value: String(c.id), label: c.name }))
	);

	function fail(e: unknown) {
		error = e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	const cents = (dollars: string) => Math.round(Number(dollars || 0) * 100);
	const currentRent = (l: Lease) => l.rent_changes.at(-1)?.amount ?? 0;

	async function load() {
		const seq = ++loadSeq;
		const slug = org;
		const url = base;
		const [p, c] = await Promise.all([
			api<Property[]>('GET', `${url}/properties`),
			api<Contact[]>('GET', `${url}/contacts`)
		]);
		if (seq !== loadSeq || slug !== org) return;
		// shortcut: one request per property; batch when counts exceed a handful.
		const allLeases = (
			await Promise.all(
				p.map((property) => api<Lease[]>('GET', `${url}/leases?property_id=${property.id}`))
			)
		).flat();
		if (seq !== loadSeq || slug !== org) return;
		properties = p;
		contacts = c;
		leases = allLeases;
		loadedOrg = slug ?? '';
		if (leaseProperty === '' && properties[0]) leaseProperty = String(properties[0].id);
		if (leaseTenant === '' && tenantOptions[0]) leaseTenant = tenantOptions[0].value;
	}

	$effect(() => {
		const slug = org;
		const allowed = canRead;
		untrack(() => {
			loadedOrg = '';
			properties = [];
			contacts = [];
			leases = [];
			propName = '';
			propAddress = '';
			propResidential = true;
			leaseProperty = '';
			leaseTenant = '';
			leaseStart = todayLocal();
			leaseFrequency = 'weekly';
			leaseRent = '';
			leaseBond = '';
			coTenants = [];
			paidTo = '';
			openingArrears = '';
			busy = false;
			error = '';
			if (allowed && slug) load().catch((e) => { if (slug === org) fail(e); });
		});
		return () => { loadSeq++; actionSeq++; };
	});

	async function act(path: string, body: unknown, after: () => void) {
		if (!canWrite || loadedOrg !== org || busy) return;
		const slug = org;
		const token = ++actionSeq;
		busy = true;
		error = '';
		try {
			await api('POST', `${base}/${path}`, body);
			if (token !== actionSeq || slug !== org) return;
			after();
			await load();
		} catch (e) {
			if (token === actionSeq && slug === org) fail(e);
		} finally {
			if (token === actionSeq) busy = false;
		}
	}

	function createProperty(e: SubmitEvent) {
		e.preventDefault();
		act('properties', {
				name: propName,
				address: propAddress || undefined,
				residential: propResidential
			}, () => {
			propName = '';
			propAddress = '';
		});
	}

	function createLease(e: SubmitEvent) {
		e.preventDefault();
		act('leases', {
				property_id: Number(leaseProperty),
				tenants: [
					{ contact_id: Number(leaseTenant), primary: true },
					...coTenants.filter((id) => id !== leaseTenant).map((id) => ({ contact_id: Number(id), primary: false }))
				],
				start_date: leaseStart,
				frequency: leaseFrequency,
				rent_amount: cents(leaseRent),
				bond_amount: cents(leaseBond) || undefined,
				paid_to_date: paidTo || undefined,
				opening_arrears: openingArrears === '' ? undefined : cents(openingArrears)
			}, () => {
			leaseRent = '';
			leaseBond = '';
			coTenants = [];
			paidTo = '';
			openingArrears = '';
		});
	}
</script>

<PageHeader title="Properties" />

{#if !canRead}
	<p role="alert">You do not have permission to view properties.</p>
{:else if loadedOrg !== org}
	{#if !error}<p role="status">Loading properties…</p>{/if}
{:else}

{#if properties.length === 0}
	<p>No properties yet.</p>
{/if}
{#each properties as p (p.id)}
	<section class="property">
		<h2>{p.name}</h2>
		{#if p.address}<p class="muted">{p.address}</p>{/if}
		{#each leases.filter((l) => l.property_id === p.id) as l (l.id)}
			<p>
				<a href={`/${page.params.org}/leases/${l.id}`}>
					Lease from {l.start_date}{l.termination_date ? ` to ${l.termination_date}` : ''}
				</a>
				· {l.tenants.map((t) => contactName.get(t.contact_id) ?? t.contact_id).join(', ')}
				· {money(currentRent(l))} {l.frequency}
			</p>
		{:else}
			<p class="muted">No leases.</p>
		{/each}
	</section>
{/each}

{#if canWrite}
	<section>
		<h2>New property</h2>
		<form onsubmit={createProperty}>
			<fieldset class="row" disabled={busy}>
			<legend>Property details</legend>
			<Field label="Name" id="prop-name"><input id="prop-name" bind:value={propName} required /></Field>
			<Field label="Address" id="prop-address"><input id="prop-address" bind:value={propAddress} /></Field>
			<label class="check"><input type="checkbox" bind:checked={propResidential} /> Residential</label>
			<Button type="submit" disabled={busy}>Create property</Button>
			</fieldset>
		</form>
	</section>

	{#if properties.length > 0}
		<section>
			<h2>New lease</h2>
			{#if tenantOptions.length === 0}
				<p>Add a contact of kind "tenant" first.</p>
			{:else}
				<form onsubmit={createLease}>
					<fieldset class="row" disabled={busy}>
					<legend>Lease details</legend>
					<Field label="Property" id="lease-property">
						<Select id="lease-property" bind:value={leaseProperty} options={propertyOptions} />
					</Field>
					<Field label="Tenant" id="lease-tenant">
						<Select id="lease-tenant" bind:value={leaseTenant} options={tenantOptions} onchange={(value) => { coTenants = coTenants.filter((id) => id !== value); }} disabled={busy} />
					</Field>
					<fieldset disabled={busy}>
						<legend>Co-tenants (optional)</legend>
						<p>The selected Tenant is primary and receives invoices.</p>
						{#each tenantOptions.filter((tenant) => tenant.value !== leaseTenant) as tenant (tenant.value)}
							<label><input type="checkbox" value={tenant.value} bind:group={coTenants} /> Co-tenant: {tenant.label}</label>
						{/each}
					</fieldset>
					<Field label="Start" id="lease-start">
						<input id="lease-start" type="date" bind:value={leaseStart} required />
					</Field>
					<Field label="Frequency" id="lease-frequency">
						<Select id="lease-frequency" bind:value={leaseFrequency} options={FREQUENCIES} />
					</Field>
					<Field label="Rent ($)" id="lease-rent">
						<input id="lease-rent" type="number" min="0" step="0.01" bind:value={leaseRent} required />
					</Field>
					<Field label="Bond ($)" id="lease-bond">
						<input id="lease-bond" type="number" min="0" step="0.01" bind:value={leaseBond} />
					</Field>
					<Field label="Paid-to date" id="lease-paid-to" hint="Must be the last day of a rent period; leave blank for a new lease.">
						<input id="lease-paid-to" type="date" bind:value={paidTo} disabled={busy} />
					</Field>
					<Field label="Opening arrears ($)" id="lease-arrears" hint="Positive arrears create a separate draft invoice; it is not sent automatically by this form.">
						<input id="lease-arrears" type="number" min="0" step="0.01" bind:value={openingArrears} disabled={busy} />
					</Field>
					<Button type="submit" disabled={busy}>Create lease</Button>
					</fieldset>
				</form>
			{/if}
		</section>
	{/if}
{/if}
{/if}

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

<style>
	fieldset { border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-4); }
	section {
		margin-bottom: var(--space-8, 2rem);
	}
	.muted {
		color: var(--text-muted, #666);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-4, 1rem);
		align-items: flex-end;
	}
	.check {
		display: flex;
		gap: var(--space-2, 0.5rem);
		align-items: center;
	}
</style>
