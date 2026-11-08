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

<div class="flex flex-col items-center gap-2 px-4 py-8 text-center text-base-content/70">
	<p class="m-0 font-semibold text-base-content">{title}</p>
	{#if description}
		<p class="m-0">{description}</p>
	{/if}
	{#if action}
		<div>{@render action()}</div>
	{/if}
</div>
