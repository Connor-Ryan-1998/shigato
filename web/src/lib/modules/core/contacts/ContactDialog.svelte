<script lang="ts" module>
	export type Contact = { id: number; kind: string; name: string; email?: string; phone?: string; company_name?: string; abn?: string };
</script>

<script lang="ts">
	import { untrack } from 'svelte';
	import { api, ApiError } from '../../../api';
	import Dialog from '../../../components/Dialog.svelte';
	import Field from '../../../components/Field.svelte';
	import Select from '../../../components/Select.svelte';
	import FormActions from '../../../components/FormActions.svelte';
	let { open = $bindable(false), org, contact = null, defaultKind = 'tenant', allowed = true, onsaved }: {
		open?: boolean; org: string; contact?: Contact | null; defaultKind?: string; allowed?: boolean;
		onsaved: (contact: Contact) => void;
	} = $props();
	const blank = () => ({ kind: defaultKind, name: '', email: '', phone: '', company_name: '', abn: '' });
	let draft = $state(blank());
	let error = $state('');
	let busy = $state(false);
	let formOrg = '';
	let seq = 0;
	$effect(() => {
		org; allowed;
		if (open) untrack(() => {
			formOrg = org;
			draft = contact ? { kind: contact.kind, name: contact.name, email: contact.email ?? '', phone: contact.phone ?? '', company_name: contact.company_name ?? '', abn: contact.abn ?? '' } : blank();
			error = '';
			busy = false;
		});
		return () => { seq++; };
	});
	async function save(event: SubmitEvent) {
		event.preventDefault();
		if (busy || !open || !allowed || formOrg !== org) return;
		const token = seq;
		const slug = org;
		const id = contact?.id;
		busy = true;
		error = '';
		try {
			const { kind, ...details } = draft;
			const result = await api<Contact>(id ? 'PUT' : 'POST', `/api/v1/orgs/${slug}/contacts${id ? `/${id}` : ''}`, id ? details : { kind, ...details });
			if (token !== seq || slug !== org || !allowed || !open) return;
			open = false;
			onsaved(result);
		} catch (e) {
			if (token === seq && slug === org && open) error = e instanceof ApiError ? e.message : 'Could not save contact.';
		} finally {
			if (token === seq) busy = false;
		}
	}
</script>

<Dialog bind:open title={contact ? 'Edit contact' : 'New contact'} {busy}>
	{#snippet children({ close })}
	{#if open}
		<form onsubmit={save} class="grid gap-4">
			{#if contact}
				<p>Kind: {contact.kind}. Contact kind cannot be changed.</p>
				<p>Changing contact details does not transfer or revoke portal access, change an existing user's login email, or update existing Stripe customer or invoice details.</p>
			{/if}
			{#if error}<p role="alert" class="alert alert-error alert-soft">{error}</p>{/if}
			<fieldset disabled={busy} class="grid min-w-0 gap-3">
				{#if !contact}<Field label="Contact kind" id="contact-kind"><Select id="contact-kind" bind:value={draft.kind} options={[{ value: 'tenant', label: 'Tenant' }, { value: 'client', label: 'Client' }, { value: 'supplier', label: 'Supplier' }]} /></Field>{/if}
				<Field label="Name" id="contact-name"><input class="input w-full" id="contact-name" bind:value={draft.name} required /></Field>
				<Field label="Email" id="contact-email"><input class="input w-full" id="contact-email" type="email" bind:value={draft.email} /></Field>
				<Field label="Phone" id="contact-phone"><input class="input w-full" id="contact-phone" type="tel" bind:value={draft.phone} /></Field>
				<Field label="Company name" id="contact-company"><input class="input w-full" id="contact-company" bind:value={draft.company_name} /></Field>
				<Field label="ABN" id="contact-abn"><input class="input w-full" id="contact-abn" bind:value={draft.abn} /></Field>
			</fieldset>
			<FormActions {busy} submitLabel={contact ? 'Save contact' : 'Create contact'} busyLabel={contact ? 'Saving…' : 'Creating…'} oncancel={close} />
		</form>
	{/if}
	{/snippet}
</Dialog>
