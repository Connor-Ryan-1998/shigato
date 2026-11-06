<script lang="ts">
	import type { Snippet } from 'svelte';

	// Same native <dialog> trick as Dialog.svelte, styled as an off-canvas
	// panel instead of a centred card: showModal() still gives focus trap
	// and Esc-to-close for free.
	let {
		open = $bindable(false),
		label,
		children
	}: {
		open?: boolean;
		label: string;
		children: Snippet;
	} = $props();

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
	}

	// A native <dialog>'s backdrop isn't a real DOM node, so a click there
	// can't get its own listener; the browser instead dispatches the click
	// at the <dialog> element itself when it lands outside the dialog's own
	// box (the drawer panel), which is what this checks.
	function handleBackdropClick(e: MouseEvent) {
		if (e.target === dialogEl) {
			open = false;
		}
	}
</script>

<dialog
	bind:this={dialogEl}
	onclose={handleClose}
	onclick={handleBackdropClick}
	aria-label={label}
	class="drawer"
>
	<nav aria-label="Main">
		{@render children()}
	</nav>
</dialog>

<style>
	.drawer {
		position: fixed;
		inset: 0 auto 0 0;
		margin: 0;
		height: 100%;
		max-height: 100%;
		width: min(16rem, 80vw);
		border: none;
		border-right: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		padding: var(--space-4);
	}
	.drawer::backdrop {
		background: rgb(0 0 0 / 0.5);
	}
</style>
