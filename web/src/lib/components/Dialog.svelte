<script lang="ts">
	import { tick, type Snippet } from 'svelte';
	import { canDismiss, formValues } from '../forms/dismiss';
	import { useUnsavedChanges } from '../forms/unsaved.svelte';

	// A native <dialog> opened with showModal() traps focus and closes on
	// Esc by itself - no hand-rolled focus trap needed.
	let {
		open = $bindable(false),
		title,
		busy = false,
		dirty,
		children,
		onclose
	}: {
		open?: boolean;
		title: string;
		busy?: boolean;
		dirty?: boolean;
		children: Snippet<[{ close: () => void }]>;
		onclose?: () => void;
	} = $props();

	// Unique per instance, so two Dialogs on the same page never collide on
	// id="dialog-title".
	const uid = $props.id();
	const titleId = `dialog-title-${uid}`;

	let dialogEl: HTMLDialogElement | undefined;
	let baseline = '';
	let changed = $state(false);
	let opener: HTMLElement | null = null;
	useUnsavedChanges(() => open && (dirty ?? changed), () => open && busy);

	$effect(() => {
		if (!dialogEl) return;
		if (open && !dialogEl.open) {
			opener = document.activeElement as HTMLElement;
			changed = false;
			dialogEl.showModal();
			tick().then(() => { if (dialogEl?.open) baseline = formValues(dialogEl); });
		} else if (!open && dialogEl.open) {
			dialogEl.close();
		}
	});

	function handleClose() {
		open = false;
		changed = false;
		if (opener?.isConnected) opener.focus();
		onclose?.();
	}

	function close() {
		if (canDismiss(busy, dirty ?? changed)) open = false;
	}
	function cancel(event: Event) {
		event.preventDefault();
		close();
	}
	function track() {
		if (dialogEl) changed = formValues(dialogEl) !== baseline;
	}
</script>

<dialog bind:this={dialogEl} onclose={handleClose} oncancel={cancel} oninput={track} onchange={track} aria-labelledby={titleId} aria-busy={busy || undefined} class="modal">
	<div class="modal-box">
		<h2 id={titleId} class="mb-4 text-lg font-bold">{title}</h2>
		{@render children({ close })}
	</div>
</dialog>
