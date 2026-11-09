<script lang="ts" module>
	export type PortalContact = { id: number; name: string; kind: string };
	export type PortalInviteResult = { emailed: boolean; link?: string; message?: string };
</script>

<script lang="ts">
	import Dialog from '../../../components/Dialog.svelte';
	import Field from '../../../components/Field.svelte';
	import Select from '../../../components/Select.svelte';
	import Button from '../../../components/Button.svelte';

	let {
		open = $bindable(false),
		contacts,
		error,
		busy = false,
		// Set once the invite request succeeds. emailed=true means the link
		// went to the contact's own inbox - nothing else to show. Otherwise
		// either `link` (share it by hand) or `message` (Ruling 60: the
		// contact's email already belongs to an account in another org, so
		// no link is ever handed back here) is present, never both.
		result = null,
		onSubmit,
		onclose
	}: {
		open?: boolean;
		contacts: PortalContact[];
		error?: string;
		busy?: boolean;
		result?: PortalInviteResult | null;
		onSubmit: (contactId: number) => void;
		onclose?: () => void;
	} = $props();

	let contactId = $state('');
	let copied = $state(false);

	// Re-seed every time the dialog opens, so a dialog reopened after a
	// previous invite never shows the last invite's result or selection.
	$effect(() => {
		if (!open) return;
		contactId = contacts[0] ? String(contacts[0].id) : '';
		copied = false;
	});

	const options = $derived(
		contacts.map((c) => ({ value: String(c.id), label: `${c.name} (${c.kind})` }))
	);

	function submit() {
		if (contactId === '') return;
		onSubmit(Number(contactId));
	}

	async function copyLink() {
		if (!result?.link) return;
		try {
			await navigator.clipboard.writeText(result.link);
			copied = true;
		} catch {
			copied = false;
		}
	}
</script>

<Dialog bind:open title="Invite to portal" {onclose} {busy} dirty={result ? false : undefined}>
	{#snippet children({ close })}
	{#if result}
		{#if result.emailed}
			<p>Invite sent.</p>
		{:else if result.link}
			<p>Invite created. Share this link with them:</p>
			<div class="mb-4 flex gap-2">
				<input class="input flex-1" readonly value={result.link} aria-label="Portal invite link" />
				<Button variant="secondary" onclick={copyLink}>{copied ? 'Copied' : 'Copy'}</Button>
			</div>
		{:else if result.message}
			<p>{result.message}</p>
		{/if}
		<div class="flex justify-end gap-3">
			<Button onclick={() => (open = false)}>Done</Button>
		</div>
	{:else if contacts.length === 0}
		<p>No tenant or client contacts yet.</p>
		<div class="flex justify-end gap-3">
			<Button variant="secondary" onclick={() => (open = false)}>Close</Button>
		</div>
	{:else}
		<fieldset disabled={busy} class="mb-4 flex flex-col gap-3">
			<Field label="Contact" id="portal-invite-contact">
				<Select id="portal-invite-contact" bind:value={contactId} {options} />
			</Field>
		</fieldset>
		{#if error}
			<p class="alert alert-error alert-soft mb-4" role="alert">{error}</p>
		{/if}
		<div class="flex justify-end gap-3">
			<Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button>
			<Button onclick={submit} disabled={contactId === '' || busy}>Invite</Button>
		</div>
	{/if}
	{/snippet}
</Dialog>
