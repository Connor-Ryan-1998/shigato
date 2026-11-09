<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		variant = 'primary',
		type = 'button',
		disabled = false,
		busy = false,
		size = 'md',
		onclick,
		children
	}: {
		variant?: 'primary' | 'secondary' | 'danger';
		type?: 'button' | 'submit' | 'reset';
		disabled?: boolean;
		busy?: boolean;
		size?: 'sm' | 'md';
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	} = $props();
</script>

<button
	class={[
		'btn',
		size === 'sm' && 'btn-sm',
		variant === 'primary' && 'btn-primary',
		variant === 'secondary' && 'btn-outline',
		variant === 'danger' && 'btn-error'
	]}
	{type}
	disabled={disabled || busy}
	aria-busy={busy || undefined}
	onclick={(event) => { if (!disabled && !busy) onclick?.(event); }}
>
	{@render children()}
</button>
