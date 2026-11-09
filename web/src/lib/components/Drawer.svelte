<script lang="ts">
	import { tick, type Snippet } from 'svelte';
	import { canDismiss, formValues } from '../forms/dismiss';
	import { useUnsavedChanges } from '../forms/unsaved.svelte';

	// Same native <dialog> trick as Dialog.svelte, styled as an off-canvas
	// panel instead of a centred card: showModal() still gives focus trap
	// and Esc-to-close for free.
	let {
		open = $bindable(false),
		label,
		side = 'left',
		width = '16rem',
		busy = false,
		dirty,
		children
	}: {
		open?: boolean;
		label: string;
		// 'left' is the mobile nav drawer's own slide-in side; 'right' is
		// used for a details/record panel (e.g. reconciliation's line
		// drawer), which conventionally opens from the trailing edge.
		side?: 'left' | 'right';
		width?: string;
		busy?: boolean;
		dirty?: boolean;
		children: Snippet<[{ close: () => void }]>;
	} = $props();

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

	// A native <dialog>'s backdrop isn't a real DOM node, so a click there
	// can't get its own listener; the browser instead dispatches the click
	// at the <dialog> element itself when it lands outside the dialog's own
	// box (the drawer panel), which is what this checks.
	function handleBackdropClick(e: MouseEvent) {
		if (e.target === dialogEl) {
			close();
		}
	}
</script>

<dialog
	bind:this={dialogEl}
	onclose={handleClose}
	oncancel={cancel}
	oninput={track}
	onchange={track}
	aria-busy={busy || undefined}
	onclick={handleBackdropClick}
	aria-label={label}
	class={[
		'fixed m-0 h-full max-h-full overflow-y-auto bg-base-100 p-4 text-base-content backdrop:bg-black/50',
		side === 'left' ? 'inset-y-0 left-0 right-auto border-r border-base-300' : 'inset-y-0 right-0 left-auto border-l border-base-300'
	]}
	style:width="min({width}, 90vw)"
>
	{@render children({ close })}
</dialog>
