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

<dialog bind:this={dialogEl} onclose={handleClose} aria-labelledby="dialog-title">
	<h2 id="dialog-title">{title}</h2>
	{@render children()}
</dialog>

<style>
	dialog {
		border: 1px solid var(--border);
		border-radius: var(--radius-lg);
		background: var(--surface-raised);
		color: var(--text);
		padding: var(--space-6);
		max-width: 32rem;
		width: calc(100% - var(--space-8));
		box-shadow: var(--shadow-lg);
	}
	dialog::backdrop {
		background: rgb(0 0 0 / 0.5);
	}
	h2 {
		margin: 0 0 var(--space-4);
		font-size: var(--text-lg);
	}
</style>
