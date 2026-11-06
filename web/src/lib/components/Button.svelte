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

<button class="btn {variant}" {type} {disabled} {onclick}>
	{@render children()}
</button>

<style>
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-4);
		border-radius: var(--radius-md);
		border: 1px solid transparent;
		font: inherit;
		font-weight: 600;
		cursor: pointer;
	}
	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.primary {
		background: var(--accent);
		color: var(--accent-contrast);
	}
	.primary:hover:not(:disabled) {
		filter: brightness(0.92);
	}
	.secondary {
		background: var(--surface);
		color: var(--text);
		border-color: var(--border);
	}
	.secondary:hover:not(:disabled) {
		background: var(--surface-raised);
	}
	.danger {
		background: var(--danger);
		color: var(--accent-contrast);
	}
	.danger:hover:not(:disabled) {
		filter: brightness(0.92);
	}
</style>
