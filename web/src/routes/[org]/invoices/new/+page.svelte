<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../lib/api';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Field from '../../../../lib/components/Field.svelte';
	import Select from '../../../../lib/components/Select.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import Dialog from '../../../../lib/components/Dialog.svelte';
	import Toast from '../../../../lib/components/Toast.svelte';
	import LineEditor, {
		type ManualLine
	} from '../../../../lib/modules/core/invoices/LineEditor.svelte';

	type Contact = { id: number; name: string; kind: string };

	const CONTACT_KIND_OPTIONS = [
		{ value: 'client', label: 'Client' },
		{ value: 'tenant', label: 'Tenant' },
		{ value: 'supplier', label: 'Supplier' }
	];

	let contacts = $state<Contact[]>([]);
	let contactId = $state('');
	let dueInDays = $state('14');
	let loadError = $state('');
	let saveError = $state('');
	let saving = $state(false);

	let newContactOpen = $state(false);
	let newContactName = $state('');
	let newContactKind = $state('client');
	let creatingContact = $state(false);

	async function loadContacts() {
		try {
			const org = page.params.org;
			contacts = await api<Contact[]>('GET', `/api/v1/orgs/${org}/contacts`);
			if (contacts.length > 0 && contactId === '') {
				contactId = String(contacts[0].id);
			}
		} catch (e) {
			loadError = e instanceof ApiError ? e.message : 'Something went wrong.';
		}
	}

	onMount(loadContacts);

	const contactOptions = $derived(contacts.map((c) => ({ value: String(c.id), label: c.name })));

	async function createContact() {
		if (newContactName.trim() === '') return;
		creatingContact = true;
		try {
			const org = page.params.org;
			const created = await api<Contact>('POST', `/api/v1/orgs/${org}/contacts`, {
				kind: newContactKind,
				name: newContactName
			});
			contacts = [...contacts, created];
			contactId = String(created.id);
			newContactOpen = false;
			newContactName = '';
			newContactKind = 'client';
		} catch (e) {
			saveError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			creatingContact = false;
		}
	}

	async function save(lines: ManualLine[]) {
		if (saving) return;
		if (contactId === '') {
			saveError = 'Choose a contact first.';
			return;
		}
		const days = Number(dueInDays);
		if (!Number.isInteger(days) || days < 0) {
			saveError = 'Due in days must be a non-negative whole number.';
			return;
		}
		saving = true;
		saveError = '';
		try {
			const org = page.params.org;
			const created = await api<{ id: number }>('POST', `/api/v1/orgs/${org}/invoices`, {
				contact_id: Number(contactId),
				source: 'manual',
				due_in_days: days,
				lines
			});
			goto(`/${org}/invoices/${created.id}`);
		} catch (e) {
			saveError = e instanceof ApiError ? e.message : 'Something went wrong.';
			saving = false;
		}
	}
</script>

<PageHeader title="New invoice" />

{#if loadError}
	<Toast message={loadError} tone="danger" />
{:else}
	<div class="form">
		<Field label="Contact" id="contact-select">
			<div class="contact-row">
				<Select id="contact-select" bind:value={contactId} options={contactOptions} />
				<Button variant="secondary" onclick={() => (newContactOpen = true)}>New contact</Button>
			</div>
		</Field>

		<Field label="Due in days" id="due-in-days" hint="Default 14 days from issue.">
			<input id="due-in-days" type="number" min="0" step="1" bind:value={dueInDays} />
		</Field>

		<LineEditor onSave={save} />
	</div>
{/if}

{#if saveError}
	<Toast message={saveError} tone="danger" onDismiss={() => (saveError = '')} />
{/if}

<Dialog bind:open={newContactOpen} title="New contact">
	<div class="fields">
		<Field label="Name" id="new-contact-name">
			<input id="new-contact-name" bind:value={newContactName} />
		</Field>
		<Field label="Kind" id="new-contact-kind">
			<Select id="new-contact-kind" bind:value={newContactKind} options={CONTACT_KIND_OPTIONS} />
		</Field>
	</div>
	<div class="dialog-actions">
		<Button variant="secondary" onclick={() => (newContactOpen = false)}>Cancel</Button>
		<Button onclick={createContact} disabled={creatingContact || newContactName.trim() === ''}>
			Create
		</Button>
	</div>
</Dialog>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
		max-width: 48rem;
	}
	.contact-row {
		display: flex;
		gap: var(--space-3);
		align-items: center;
	}
	.fields {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		margin-bottom: var(--space-4);
	}
	.dialog-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-3);
	}
</style>
