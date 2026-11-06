<script lang="ts">
	import type { Snippet } from 'svelte';

	// Field owns the <label for=id>; children renders the actual input so
	// every input in the app has a real, associated label (never a
	// placeholder standing in for one).
	let {
		label,
		id,
		hint,
		error,
		children
	}: {
		label: string;
		id: string;
		hint?: string;
		error?: string;
		children: Snippet;
	} = $props();
</script>

<div class="field">
	<label for={id}>{label}</label>
	{@render children()}
	{#if error}
		<p class="error" role="alert">{error}</p>
	{:else if hint}
		<p class="hint">{hint}</p>
	{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
	}
	label {
		font-weight: 600;
		font-size: var(--text-sm);
	}
	.hint {
		margin: 0;
		font-size: var(--text-sm);
		color: var(--text-muted);
	}
	.error {
		margin: 0;
		font-size: var(--text-sm);
		color: var(--danger);
	}
</style>
