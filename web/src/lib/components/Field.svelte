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

<div class="fieldset py-0">
	<label for={id} class="fieldset-legend pb-0">{label}</label>
	{@render children()}
	{#if error}
		<p class="m-0 text-sm text-error" role="alert">{error}</p>
	{:else if hint}
		<p class="m-0 text-sm text-base-content/70">{hint}</p>
	{/if}
</div>
