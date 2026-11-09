<script lang="ts">
	import Button from './Button.svelte';
	let { busy = false, submitLabel, busyLabel = 'Saving…', oncancel }: {
		busy?: boolean; submitLabel: string; busyLabel?: string; oncancel?: () => void;
	} = $props();
</script>

<div class="mt-4 flex flex-wrap justify-end gap-2">
	{#if oncancel}<Button variant="secondary" disabled={busy} onclick={oncancel}>Cancel</Button>{/if}
	<Button type="submit" {busy}>{busy ? busyLabel : submitLabel}</Button>
</div>
