<script lang="ts">
	import type { Snippet } from 'svelte';

	// Same native <dialog> trick as Dialog.svelte, styled as an off-canvas
	// panel instead of a centred card: showModal() still gives focus trap
	// and Esc-to-close for free.
	let {
		open = $bindable(false),
		label,
		side = 'left',
		width = '16rem',
		children
	}: {
		open?: boolean;
		label: string;
		// 'left' is the mobile nav drawer's own slide-in side; 'right' is
		// used for a details/record panel (e.g. reconciliation's line
		// drawer), which conventionally opens from the trailing edge.
		side?: 'left' | 'right';
		width?: string;
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
	class="drawer {side}"
	style:width="min({width}, 90vw)"
>
	{@render children()}
</dialog>

<style>
	.drawer {
		position: fixed;
		inset: 0 0 0 auto;
		margin: 0;
		height: 100%;
		max-height: 100%;
		overflow-y: auto;
		border: none;
		background: var(--surface);
		color: var(--text);
		padding: var(--space-4);
	}
	.drawer.left {
		inset: 0 auto 0 0;
		border-right: 1px solid var(--border);
	}
	.drawer.right {
		inset: 0 0 0 auto;
		border-left: 1px solid var(--border);
	}
	.drawer::backdrop {
		background: rgb(0 0 0 / 0.5);
	}
</style>
