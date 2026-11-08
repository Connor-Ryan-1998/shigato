<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import DataTable from '../../../lib/components/DataTable.svelte';

	type Contact = {
		id: number;
		kind: 'tenant' | 'client' | 'supplier';
		name: string;
		email?: string;
		phone?: string;
		company_name?: string;
		abn?: string;
	};
	const kinds = [
		{ value: 'tenant', label: 'Tenant' },
		{ value: 'client', label: 'Client' },
		{ value: 'supplier', label: 'Supplier' }
	];
	const columns = [
		{ key: 'name', label: 'Name' }, { key: 'kind', label: 'Kind' },
		{ key: 'email', label: 'Email' }, { key: 'phone', label: 'Phone' },
		{ key: 'company_name', label: 'Company' }, { key: 'abn', label: 'ABN' }
	];
	const emptyDraft = () => ({ kind: 'tenant', name: '', email: '', phone: '', company_name: '', abn: '' });
	const org = $derived(page.params.org);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(membership?.perms.includes('invoices_read') ?? false);
	const canWrite = $derived(membership?.perms.includes('contacts_write') ?? false);
	let filter = $state('');
	let contacts = $state<Contact[]>([]);
	let loadedOrg = $state('');
	let loadedKind = $state('');
	let loading = $state(false);
	let loadError = $state('');
	let draft = $state(emptyDraft());
	let editingID = $state<number | null>(null);
	let formOrg = $state('');
	let creating = $state(false);
	let createError = $state('');
	let success = $state('');
	let requestSeq = 0;
	let createSeq = 0;

	async function load(slug: string, kind: string) {
		const seq = ++requestSeq;
		contacts = [];
		loadedOrg = '';
		loadError = '';
		loading = true;
		try {
			const result = await api<Contact[]>('GET', `/api/v1/orgs/${slug}/contacts${kind ? `?kind=${kind}` : ''}`);
			if (seq !== requestSeq || slug !== org || kind !== filter || !canRead) return;
			contacts = result;
		} catch (e) {
			if (seq !== requestSeq || slug !== org || kind !== filter || !canRead) return;
			loadError = e instanceof ApiError ? e.message : 'Could not load contacts.';
		} finally {
			if (seq === requestSeq && slug === org && kind === filter && canRead) {
				loadedOrg = slug;
				loadedKind = kind;
				loading = false;
			}
		}
	}

	$effect(() => {
		if (canRead && org) load(org, filter);
		return () => { requestSeq++; };
	});

	$effect(() => {
		formOrg = canWrite ? org ?? '' : '';
		draft = emptyDraft();
		editingID = null;
		createSeq++;
		creating = false;
		createError = '';
		success = '';
	});

	function edit(contact: Contact) {
		if (!canWrite || !canRead || formOrg !== org || loadedOrg !== org || loadedKind !== filter || creating) return;
		editingID = contact.id;
		draft = { kind: contact.kind, name: contact.name, email: contact.email ?? '', phone: contact.phone ?? '', company_name: contact.company_name ?? '', abn: contact.abn ?? '' };
		createError = '';
		success = '';
	}

	function cancelEdit() {
		if (creating) return;
		editingID = null;
		draft = emptyDraft();
		createError = '';
	}

	async function create(e: SubmitEvent) {
		e.preventDefault();
		if (!canWrite || !canRead || !org || formOrg !== org || creating) return;
		const slug = org;
		const id = editingID;
		const seq = ++createSeq;
		creating = true;
		createError = '';
		success = '';
		try {
			const { kind, ...details } = draft;
			await api<Contact>(id === null ? 'POST' : 'PUT', `/api/v1/orgs/${slug}/contacts${id === null ? '' : `/${id}`}`, id === null ? { kind, ...details } : details);
			if (seq !== createSeq || slug !== org || !canWrite) return;
			draft = emptyDraft();
			editingID = null;
			success = id === null ? 'Contact created.' : 'Contact updated.';
			await load(slug, filter);
		} catch (e) {
			if (seq !== createSeq || slug !== org || !canWrite) return;
			createError = e instanceof ApiError ? e.message : id === null ? 'Could not create contact.' : 'Could not update contact.';
		} finally {
			if (seq === createSeq) creating = false;
		}
	}
</script>

<PageHeader title="Contacts" />

{#snippet contactActions(contact: Contact)}
	<Button variant="secondary" disabled={creating} onclick={() => edit(contact)}>Edit {contact.name}</Button>
{/snippet}

{#if !canRead}
	<p role="alert">You do not have permission to view contacts.</p>
{:else}
	<Field label="Filter by kind" id="contact-filter">
		<Select id="contact-filter" bind:value={filter} options={[{ value: '', label: 'All' }, ...kinds]} />
	</Field>
	{#if loading || loadedOrg !== org || loadedKind !== filter}
		<p role="status">Loading contacts…</p>
	{:else if loadError}
		<p role="alert" class="alert alert-error alert-soft mb-2">{loadError}</p>
		<Button onclick={() => org && load(org, filter)}>Retry</Button>
	{:else if contacts.length === 0}
		<p>No contacts found.</p>
	{:else}
		<DataTable columns={canWrite ? [...columns, { key: 'actions', label: 'Actions', cell: contactActions }] : columns} rows={contacts} rowKey={(contact) => contact.id} />
	{/if}

	{#if canWrite && formOrg === org}
		<section class="card mt-6 bg-base-100 shadow-sm">
			<div class="card-body">
			<h2 class="card-title">{editingID === null ? 'New contact' : 'Edit contact'}</h2>
			{#if editingID !== null}
				<p>Kind: {draft.kind}. Contact kind cannot be changed.</p>
				<p>Changing contact details does not transfer or revoke portal access, change an existing user's login email, or update existing Stripe customer or invoice details.</p>
			{/if}
			{#if createError}<p role="alert" class="alert alert-error alert-soft">{createError}</p>{/if}
			{#if success}<p role="status" class="alert alert-success alert-soft">{success}</p>{/if}
			<form onsubmit={create} class="grid max-w-2xl gap-4">
				<fieldset disabled={creating} class="fieldset grid gap-3 rounded-box border border-base-300 p-4">
					<legend class="fieldset-legend">Contact details</legend>
					{#if editingID === null}<Field label="Contact kind" id="contact-kind"><Select id="contact-kind" bind:value={draft.kind} options={kinds} /></Field>{/if}
					<Field label="Name" id="contact-name"><input class="input w-full" id="contact-name" bind:value={draft.name} required /></Field>
					<Field label="Email" id="contact-email"><input class="input w-full" id="contact-email" type="email" bind:value={draft.email} /></Field>
					<Field label="Phone" id="contact-phone"><input class="input w-full" id="contact-phone" type="tel" bind:value={draft.phone} /></Field>
					<Field label="Company name" id="contact-company"><input class="input w-full" id="contact-company" bind:value={draft.company_name} /></Field>
					<Field label="ABN" id="contact-abn"><input class="input w-full" id="contact-abn" bind:value={draft.abn} /></Field>
				</fieldset>
				<Button type="submit" disabled={creating}>{creating ? editingID === null ? 'Creating…' : 'Saving…' : editingID === null ? 'Create contact' : 'Save contact'}</Button>
				{#if editingID !== null}<Button variant="secondary" disabled={creating} onclick={cancelEdit}>Cancel editing</Button>{/if}
			</form>
			</div>
		</section>
	{/if}
{/if}
