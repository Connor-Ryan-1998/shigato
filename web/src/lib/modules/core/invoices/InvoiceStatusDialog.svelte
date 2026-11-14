<script lang="ts">
	import Dialog from '../../../components/Dialog.svelte';
	import Field from '../../../components/Field.svelte';
	import Button from '../../../components/Button.svelte';

	// A short reason editor for an invoice status change (write-off or
	// reinstatement). Like PaymentDialog, the page owns the request: on a
	// failed save it passes the server's message back in `error` and the
	// dialog stays open with the reason intact.
	let {
		open = $bindable(false),
		title,
		submitLabel,
		description,
		error,
		busy = false,
		onSubmit
	}: {
		open?: boolean;
		title: string;
		submitLabel: string;
		description: string;
		error?: string;
		busy?: boolean;
		onSubmit: (reason: string) => void;
	} = $props();

	let reason = $state('');

	$effect(() => {
		if (open) reason = '';
	});

	const valid = $derived(reason.trim() !== '');

	function submit(event: SubmitEvent) {
		event.preventDefault();
		if (busy || !valid) return;
		onSubmit(reason.trim());
	}
</script>

<Dialog bind:open {title} {busy}>
	{#snippet children({ close })}
		<form onsubmit={submit} class="grid gap-4">
			<p>{description}</p>
			<fieldset disabled={busy} class="grid gap-3">
				<Field label="Reason" id="status-reason">
					<textarea class="textarea w-full" id="status-reason" maxlength="500" required bind:value={reason}></textarea>
				</Field>
			</fieldset>
			{#if error}
				<p class="alert alert-error alert-soft" role="alert">{error}</p>
			{/if}
			<div class="flex justify-end gap-3">
				<Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button>
				<Button type="submit" disabled={!valid || busy}>{submitLabel}</Button>
			</div>
		</form>
	{/snippet}
</Dialog>
