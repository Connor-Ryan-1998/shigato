<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { useUnsavedChanges } from '../../../../lib/forms/unsaved.svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../lib/api';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Field from '../../../../lib/components/Field.svelte';
	import Select from '../../../../lib/components/Select.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import Dialog from '../../../../lib/components/Dialog.svelte';
	import Toast from '../../../../lib/components/Toast.svelte';
	import ContactDialog from '../../../../lib/modules/core/contacts/ContactDialog.svelte';
	import LineEditor, {
		type ManualLine
	} from '../../../../lib/modules/core/invoices/LineEditor.svelte';

	type Contact = { id: number; name: string; kind: string };

	let contacts = $state<Contact[]>([]);
	let contactId = $state('');
	let dueInDays = $state('14');
	let loadError = $state('');
	let saveError = $state('');
	let saving = $state(false);
	let dirty = $state(false);
	let createdID = $state<number | null>(null);
	let navigationError = $state('');
	let active = true;
	onDestroy(() => { active = false; });
	const { allowLeave } = useUnsavedChanges(() => dirty, () => saving);

	let newContactOpen = $state(false);
	// Shown inside the New contact dialog itself - a page-level Toast sits
	// behind a native <dialog>'s backdrop and would be invisible while it's
	// open.

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
		newContactOpen = true;
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
		if (saving || createdID !== null) return;
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
		const slug = page.params.org;
		try {
			const org = page.params.org;
			const created = await api<{ id: number }>('POST', `/api/v1/orgs/${org}/invoices`, {
				contact_id: Number(contactId),
				source: 'manual',
				due_in_days: days,
				lines
			});
			if (!active || slug !== page.params.org) return;
			dirty = false;
			createdID = created.id;
			saving = false;
			try {
				await goto(`/${org}/invoices/${created.id}`);
			} catch {
				if (active && slug === page.params.org) navigationError = 'Invoice saved, but the detail page could not be opened. Use the saved invoice link.';
			}
		} catch (e) {
			if (!active || slug !== page.params.org) return;
			saveError = e instanceof ApiError ? e.message : 'Something went wrong.';
			saving = false;
		}
	}
</script>

<PageHeader title="New invoice" back={{ href: `/${page.params.org}/invoices`, label: 'Back to invoices' }} />

{#if createdID !== null}
	<p role="status" class="alert alert-success alert-soft">Invoice saved. <a class="link" href={`/${page.params.org}/invoices/${createdID}`}>View saved invoice</a></p>
	{#if navigationError}<p role="alert" class="alert alert-warning alert-soft mt-3">{navigationError}</p>{/if}
{:else if loadError}
	<Toast message={loadError} tone="danger" />
{:else}
	<fieldset disabled={saving} class="flex min-w-0 max-w-3xl flex-col gap-5" oninput={() => dirty = true} onchange={() => dirty = true}>
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

		<LineEditor onSave={save} busy={saving} onchange={() => dirty = true} />
		<Button variant="secondary" disabled={saving} onclick={() => { if (allowLeave()) { dirty = false; goto(`/${page.params.org}/invoices`); } }}>Cancel</Button>
	</fieldset>
{/if}

{#if saveError}
	<Toast message={saveError} tone="danger" onDismiss={() => (saveError = '')} />
{/if}

<ContactDialog bind:open={newContactOpen} org={page.params.org ?? ''} defaultKind="client" onsaved={(created) => { contacts = [...contacts, created]; contactId = String(created.id); dirty = true; }} />
