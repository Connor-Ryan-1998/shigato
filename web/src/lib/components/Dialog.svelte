<script lang="ts">
	import type { Snippet } from 'svelte';

	// A native <dialog> opened with showModal() traps focus and closes on
	// Esc by itself - no hand-rolled focus trap needed.
	let {
		open = $bindable(false),
		title,
		children,
		onclose
	}: {
		open?: boolean;
		title: string;
		children: Snippet;
		onclose?: () => void;
	} = $props();

	// Unique per instance, so two Dialogs on the same page never collide on
	// id="dialog-title".
	const uid = $props.id();
	const titleId = `dialog-title-${uid}`;

	let dialogEl: HTMLDialogElement | undefined;

	$effect(() => {
		if (!dialogEl) return;
		if (open && !dialogEl.open) {
			dialogEl.showModal();
		} else if (!open && dialogEl.open) {
			dialogEl.close();
		}
	});

	function handleClose() {
		open = false;
		onclose?.();
	}
</script>

<dialog bind:this={dialogEl} onclose={handleClose} aria-labelledby={titleId} class="modal">
	<div class="modal-box">
		<h2 id={titleId} class="mb-4 text-lg font-bold">{title}</h2>
		{@render children()}
	</div>
</dialog>
