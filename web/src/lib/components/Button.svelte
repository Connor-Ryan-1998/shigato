<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		variant = 'primary',
		type = 'button',
		disabled = false,
		onclick,
		children
	}: {
		variant?: 'primary' | 'secondary' | 'danger';
		type?: 'button' | 'submit' | 'reset';
		disabled?: boolean;
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	} = $props();
</script>

<button
	class={[
		'btn',
		variant === 'primary' && 'btn-primary',
		variant === 'secondary' && 'btn-outline',
		variant === 'danger' && 'btn-error'
	]}
	{type}
	{disabled}
	{onclick}
>
	{@render children()}
</button>
