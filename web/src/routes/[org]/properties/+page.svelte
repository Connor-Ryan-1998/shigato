<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { money } from '../../../lib/format';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import Dialog from '../../../lib/components/Dialog.svelte';
	import FormActions from '../../../lib/components/FormActions.svelte';
	import LoadState from '../../../lib/components/LoadState.svelte';
	import DataTable from '../../../lib/components/DataTable.svelte';
	type Property = { id: number; name: string; address?: string; residential: boolean };
	type Lease = { id: number; property_id: number; start_date: string; termination_date?: string; frequency: string; rent_changes: { effective_date: string; amount: number }[]; tenants: { contact_id: number; primary: boolean }[] };
	const org = $derived(page.params.org ?? '');
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('property_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('property_write') ?? false);
	let properties = $state<Property[]>([]);
	let leases = $state<Lease[]>([]);
	let contacts = $state<{ id: number; name: string }[]>([]);
	let loaded = $state('');
	let error = $state('');
	let saveError = $state('');
	let success = $state('');
	let creating = $state(false);
	let busy = $state(false);
	let name = $state('');
	let address = $state('');
	let residential = $state(true);
	let seq = 0;
	async function load() {
		const slug = org;
		const token = ++seq;
		error = '';
		try {
			const base = `/api/v1/orgs/${slug}`;
			const [p, c] = await Promise.all([api<Property[]>('GET', `${base}/properties`), api<typeof contacts>('GET', `${base}/contacts`)]);
			// shortcut: one request per property; batch when counts exceed a handful.
			const l = (await Promise.all(p.map((property) => api<Lease[]>('GET', `${base}/leases?property_id=${property.id}`)))).flat();
			if (token !== seq || slug !== org || !canRead) return;
			properties = p; contacts = c; leases = l; loaded = slug;
		} catch (e) { if (token === seq && slug === org) error = e instanceof ApiError ? e.message : 'Could not load properties.'; }
	}
	$effect(() => {
		org; canRead; canWrite;
		untrack(() => { loaded = ''; creating = false; busy = false; success = ''; if (canRead) load(); });
		return () => { seq++; };
	});
	async function save(event: SubmitEvent) {
		event.preventDefault();
		if (busy || !canWrite || !canRead || loaded !== org) return;
		const token = seq;
		const slug = org;
		busy = true; saveError = '';
		try {
			await api('POST', `/api/v1/orgs/${slug}/properties`, { name, address: address || undefined, residential });
			if (token !== seq || slug !== org || !canWrite) return;
			creating = false; success = 'Property created.'; busy = false;
			await load();
		} catch (e) { if (token === seq) saveError = e instanceof ApiError ? e.message : 'Could not create property.'; }
		finally { if (token === seq) busy = false; }
	}
</script>

<PageHeader title="Properties">
	{#snippet actions()}{#if canWrite}
		<a class="btn btn-outline" href={`/${org}/leases/new`}>New lease</a>
		<Button disabled={loaded !== org} onclick={() => { name = ''; address = ''; residential = true; saveError = ''; creating = true; }}>New property</Button>
	{/if}{/snippet}
</PageHeader>
{#snippet leaseCell(property: Property)}
	{#each leases.filter((l) => l.property_id === property.id) as lease (lease.id)}
		<div class="mb-2"><a class="link link-primary" href={`/${org}/leases/${lease.id}`}>Lease from {lease.start_date}{lease.termination_date ? ` to ${lease.termination_date}` : ''}</a>
		<p>{lease.tenants.map((t) => contacts.find((c) => c.id === t.contact_id)?.name ?? t.contact_id).join(', ')} · {money(lease.rent_changes.at(-1)?.amount ?? 0)} {lease.frequency}</p></div>
	{:else}<span class="text-base-content/70">No leases.</span>{/each}
{/snippet}
{#if !canRead}<p role="alert">You do not have permission to view properties.</p>
{:else}
	{#if success}<p role="status" class="alert alert-success alert-soft mb-4">{success}</p>{/if}
	<LoadState loading={loaded !== org} {error} empty={properties.length === 0} loadingLabel="Loading properties…" emptyTitle="No properties yet." onretry={load}>
		<DataTable columns={[{ key: 'name', label: 'Name' }, { key: 'address', label: 'Address' }, { key: 'leases', label: 'Leases', cell: leaseCell }]} rows={properties} rowKey={(p) => p.id} />
	</LoadState>
	{#if canWrite}
		<Dialog bind:open={creating} title="New property" {busy}>
			{#snippet children({ close })}{#if creating}
			<form onsubmit={save} class="grid gap-4">
				{#if saveError}<p role="alert" class="alert alert-error alert-soft">{saveError}</p>{/if}
				<fieldset disabled={busy} class="grid gap-3">
					<Field label="Name" id="prop-name"><input class="input w-full" id="prop-name" bind:value={name} required /></Field>
					<Field label="Address" id="prop-address"><input class="input w-full" id="prop-address" bind:value={address} /></Field>
					<label class="label"><input class="checkbox" type="checkbox" bind:checked={residential} /> Residential</label>
				</fieldset>
				<FormActions {busy} submitLabel="Create property" oncancel={close} />
			</form>
			{/if}{/snippet}
		</Dialog>
	{/if}
{/if}
