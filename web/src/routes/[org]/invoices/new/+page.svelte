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
	// Shown inside the New contact dialog itself - a page-level Toast sits
	// behind a native <dialog>'s backdrop and would be invisible while it's
	// open.
	let contactError = $state('');

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

	function openNewContactDialog() {
		contactError = '';
		newContactOpen = true;
	}

	async function createContact() {
		if (newContactName.trim() === '') return;
		creatingContact = true;
		contactError = '';
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
			contactError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			creatingContact = false;
		}
	}

	// parseDueInDays accepts only a plain non-negative whole number - an
	// empty/blank field must be reported as invalid, not silently treated
	// as 0 (Number('') is 0, which is why this doesn't just do
	// Number(dueInDays)).
	function parseDueInDays(v: string): number | null {
		const t = v.trim();
		if (!/^\d+$/.test(t)) return null;
		const n = Number(t);
		return Number.isSafeInteger(n) ? n : null;
	}

	const dueInDaysError = $derived(
		parseDueInDays(dueInDays) === null ? 'Enter a whole number of days (0 or more).' : undefined
	);

	async function save(lines: ManualLine[]) {
		if (saving) return;
		if (contactId === '') {
			saveError = 'Choose a contact first.';
			return;
		}
		const days = parseDueInDays(dueInDays);
		if (days === null) {
			saveError = 'Enter a whole number of days (0 or more).';
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
	<div class="flex max-w-3xl flex-col gap-5">
		<Field label="Contact" id="contact-select">
			<div class="flex items-center gap-3">
				<Select id="contact-select" bind:value={contactId} options={contactOptions} />
				<Button variant="secondary" onclick={openNewContactDialog}>New contact</Button>
			</div>
		</Field>

		<Field
			label="Due in days"
			id="due-in-days"
			hint="Default 14 days from issue."
			error={dueInDaysError}
		>
			<input class="input" id="due-in-days" inputmode="numeric" bind:value={dueInDays} />
		</Field>

		<LineEditor onSave={save} />
	</div>
{/if}

{#if saveError}
	<Toast message={saveError} tone="danger" onDismiss={() => (saveError = '')} />
{/if}

<Dialog bind:open={newContactOpen} title="New contact">
	<div class="mb-4 flex flex-col gap-3">
		<Field label="Name" id="new-contact-name">
			<input class="input w-full" id="new-contact-name" bind:value={newContactName} />
		</Field>
		<Field label="Kind" id="new-contact-kind">
			<Select id="new-contact-kind" bind:value={newContactKind} options={CONTACT_KIND_OPTIONS} />
		</Field>
	</div>
	{#if contactError}
		<p class="alert alert-error alert-soft mb-4" role="alert">{contactError}</p>
	{/if}
	<div class="modal-action">
		<Button variant="secondary" onclick={() => (newContactOpen = false)}>Cancel</Button>
		<Button onclick={createContact} disabled={creatingContact || newContactName.trim() === ''}>
			Create
		</Button>
	</div>
</Dialog>
