<script lang="ts" module>
	export type NewInvite = { email: string; role: string };
</script>

<script lang="ts">
	import Dialog from '../../../components/Dialog.svelte';
	import Field from '../../../components/Field.svelte';
	import Select from '../../../components/Select.svelte';
	import Button from '../../../components/Button.svelte';

	const ROLE_OPTIONS = [
		{ value: 'staff', label: 'Staff' },
		{ value: 'bookkeeper', label: 'Bookkeeper' },
		{ value: 'owner', label: 'Owner' }
	];

	let {
		open = $bindable(false),
		error,
		busy = false,
		// Set once the invite is created - the server always returns a link
		// regardless of whether email was sent, so the dialog switches away
		// from the form either way.
		link = '',
		// Whether the server actually sent the invite email (false covers
		// both "no mailer configured" and "the send failed"). When true, the
		// dialog shows a "sent to ..." note instead of the link/copy
		// affordance - the recipient already has it in their inbox.
		emailed = false,
		invitedEmail = '',
		onSubmit,
		onclose
	}: {
		open?: boolean;
		error?: string;
		busy?: boolean;
		link?: string;
		emailed?: boolean;
		invitedEmail?: string;
		onSubmit: (invite: NewInvite) => void;
		onclose?: () => void;
	} = $props();

	let email = $state('');
	let role = $state('staff');
	let copied = $state(false);

	// Re-seed the form every time the dialog opens, so a dialog reopened
	// after a previous invite never shows the last invite's link or email.
	$effect(() => {
		if (!open) return;
		email = '';
		role = 'staff';
		copied = false;
	});

	const valid = $derived(email.trim() !== '');

	function submit() {
		if (!valid) return;
		onSubmit({ email: email.trim(), role });
	}

	async function copyLink() {
		try {
			await navigator.clipboard.writeText(link);
			copied = true;
		} catch {
			copied = false;
		}
	}
</script>

<Dialog bind:open title="Invite a staff member" {onclose}>
	{#if link}
		{#if emailed}
			<p>Invite sent to <strong>{invitedEmail}</strong>.</p>
		{:else}
			<p>Invite created. Share this link with them:</p>
			<div class="mb-4 flex gap-2">
				<input class="input flex-1" readonly value={link} aria-label="Invite link" />
				<Button variant="secondary" onclick={copyLink}>{copied ? 'Copied' : 'Copy'}</Button>
			</div>
		{/if}
		<div class="flex justify-end gap-3">
			<Button onclick={() => (open = false)}>Done</Button>
		</div>
	{:else}
		<div class="mb-4 flex flex-col gap-3">
			<Field label="Email" id="invite-email">
				<input class="input w-full" id="invite-email" type="email" bind:value={email} autocomplete="off" />
			</Field>
			<Field label="Role" id="invite-role">
				<Select id="invite-role" bind:value={role} options={ROLE_OPTIONS} />
			</Field>
		</div>
		{#if error}
			<p class="alert alert-error alert-soft mb-4" role="alert">{error}</p>
		{/if}
		<div class="flex justify-end gap-3">
			<Button variant="secondary" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
			<Button onclick={submit} disabled={!valid || busy}>Send invite</Button>
		</div>
	{/if}
</Dialog>
