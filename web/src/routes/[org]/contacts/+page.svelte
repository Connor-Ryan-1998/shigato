<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import DataTable from '../../../lib/components/DataTable.svelte';
	import FilterBar from '../../../lib/components/FilterBar.svelte';
	import LoadState from '../../../lib/components/LoadState.svelte';
	import ContactDialog, { type Contact } from '../../../lib/modules/core/contacts/ContactDialog.svelte';
	const org = $derived(page.params.org ?? '');
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('invoices_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('contacts_write') ?? false);
	let filter = $state('');
	let contacts = $state<Contact[]>([]);
	let loaded = $state('');
	let error = $state('');
	let success = $state('');
	let editorOpen = $state(false);
	let editing = $state<Contact | null>(null);
	let seq = 0;
	const key = $derived(`${org}/${filter}`);
	async function load(slug: string, kind: string) {
		const token = ++seq;
		const scope = `${slug}/${kind}`;
		error = '';
		try {
			const result = await api<Contact[]>('GET', `/api/v1/orgs/${slug}/contacts${kind ? `?kind=${kind}` : ''}`);
			if (token !== seq || scope !== key || !canRead) return;
			contacts = result;
			loaded = scope;
		} catch (e) {
			if (token === seq && scope === key && canRead) error = e instanceof ApiError ? e.message : 'Could not load contacts.';
		}
	}
	$effect(() => { if (org && canRead) load(org, filter); return () => { seq++; }; });
	$effect(() => { org; canWrite; editorOpen = false; success = ''; editing = null; });
	function edit(contact: Contact | null) {
		if (!canRead || !canWrite || loaded !== key) return;
		editing = contact;
		editorOpen = true;
	}
	function saved() {
		success = editing ? 'Contact updated.' : 'Contact created.';
		load(org, filter);
	}
</script>

<PageHeader title="Contacts">
	{#snippet actions()}{#if canWrite}<Button disabled={loaded !== key} onclick={() => edit(null)}>New contact</Button>{/if}{/snippet}
</PageHeader>
{#snippet contactActions(contact: Contact)}<Button variant="secondary" size="sm" onclick={() => edit(contact)}>Edit {contact.name}</Button>{/snippet}
{#if !canRead}
	<p role="alert">You do not have permission to view contacts.</p>
{:else}
	<FilterBar><Field label="Filter by kind" id="contact-filter"><Select id="contact-filter" bind:value={filter} options={[{ value: '', label: 'All' }, { value: 'tenant', label: 'Tenant' }, { value: 'client', label: 'Client' }, { value: 'supplier', label: 'Supplier' }]} /></Field></FilterBar>
	{#if success}<p role="status" class="alert alert-success alert-soft mb-4">{success}</p>{/if}
	<LoadState loading={loaded !== key} {error} loadingLabel="Loading contacts…" empty={contacts.length === 0} emptyTitle="No contacts found." onretry={() => load(org, filter)}>
		<DataTable columns={[
			{ key: 'name', label: 'Name' }, { key: 'kind', label: 'Kind' }, { key: 'email', label: 'Email' },
			{ key: 'phone', label: 'Phone' }, { key: 'company_name', label: 'Company' }, { key: 'abn', label: 'ABN' },
			...(canWrite ? [{ key: 'actions', label: 'Actions', cell: contactActions }] : [])
		]} rows={contacts} rowKey={(contact) => contact.id} />
	</LoadState>
	{#if canWrite}<ContactDialog bind:open={editorOpen} {org} contact={editing} allowed={canWrite && canRead} onsaved={saved} />{/if}
{/if}
