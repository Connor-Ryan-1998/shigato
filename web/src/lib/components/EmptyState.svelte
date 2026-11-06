<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		title,
		description,
		action
	}: {
		title: string;
		description?: string;
		action?: Snippet;
	} = $props();
</script>

<div class="empty-state">
	<p class="title">{title}</p>
	{#if description}
		<p class="description">{description}</p>
	{/if}
	{#if action}
		<div class="action">{@render action()}</div>
	{/if}
</div>

<style>
	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-2);
		padding: var(--space-8) var(--space-4);
		text-align: center;
		color: var(--text-muted);
	}
	.title {
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}
	.description {
		margin: 0;
	}
</style>
