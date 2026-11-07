<script lang="ts">
	import { onMount } from 'svelte';
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

	const base = $derived(`/api/v1/orgs/${page.params.org}`);
	const canWrite = $derived(session.currentOrg?.perms.includes('property_write') ?? false);

	let properties = $state<Property[]>([]);
	let leases = $state<Lease[]>([]);
	let contacts = $state<Contact[]>([]);
	let error = $state('');

	let propName = $state('');
	let propAddress = $state('');
	let propResidential = $state(true);

	let leaseProperty = $state('');
	let leaseTenant = $state('');
	let leaseStart = $state(todayLocal());
	let leaseFrequency = $state('weekly');
	let leaseRent = $state('');
	let leaseBond = $state('');

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
		[properties, contacts] = await Promise.all([
			api<Property[]>('GET', `${base}/properties`),
			api<Contact[]>('GET', `${base}/contacts`)
		]);
		// ponytail: one request per property; add an org-wide lease list if
		// property counts grow past a handful.
		leases = (
			await Promise.all(
				properties.map((p) => api<Lease[]>('GET', `${base}/leases?property_id=${p.id}`))
			)
		).flat();
		if (leaseProperty === '' && properties[0]) leaseProperty = String(properties[0].id);
		if (leaseTenant === '' && tenantOptions[0]) leaseTenant = tenantOptions[0].value;
	}

	onMount(() => load().catch(fail));

	async function act(fn: () => Promise<unknown>) {
		error = '';
		try {
			await fn();
			await load();
		} catch (e) {
			fail(e);
		}
	}

	function createProperty(e: SubmitEvent) {
		e.preventDefault();
		act(async () => {
			await api('POST', `${base}/properties`, {
				name: propName,
				address: propAddress || undefined,
				residential: propResidential
			});
			propName = '';
			propAddress = '';
		});
	}

	function createLease(e: SubmitEvent) {
		e.preventDefault();
		act(async () => {
			await api('POST', `${base}/leases`, {
				property_id: Number(leaseProperty),
				tenants: [{ contact_id: Number(leaseTenant), primary: true }],
				start_date: leaseStart,
				frequency: leaseFrequency,
				rent_amount: cents(leaseRent),
				bond_amount: cents(leaseBond) || undefined
			});
			leaseRent = '';
			leaseBond = '';
		});
	}
</script>

<PageHeader title="Properties" />

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
		<form class="row" onsubmit={createProperty}>
			<Field label="Name" id="prop-name"><input id="prop-name" bind:value={propName} required /></Field>
			<Field label="Address" id="prop-address"><input id="prop-address" bind:value={propAddress} /></Field>
			<label class="check"><input type="checkbox" bind:checked={propResidential} /> Residential</label>
			<Button type="submit">Create property</Button>
		</form>
	</section>

	{#if properties.length > 0}
		<section>
			<h2>New lease</h2>
			{#if tenantOptions.length === 0}
				<p>Add a contact of kind "tenant" first.</p>
			{:else}
				<form class="row" onsubmit={createLease}>
					<Field label="Property" id="lease-property">
						<Select id="lease-property" bind:value={leaseProperty} options={propertyOptions} />
					</Field>
					<Field label="Tenant" id="lease-tenant">
						<Select id="lease-tenant" bind:value={leaseTenant} options={tenantOptions} />
					</Field>
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
					<Button type="submit">Create lease</Button>
				</form>
			{/if}
		</section>
	{/if}
{/if}

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

<style>
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
