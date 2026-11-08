<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		title,
		actions
	}: {
		title: string;
		actions?: Snippet;
	} = $props();
</script>

<header class="mb-6 flex flex-wrap items-center justify-between gap-4">
	<h1 class="m-0 text-2xl font-bold">{title}</h1>
	{#if actions}
		<div class="flex flex-wrap gap-2">{@render actions()}</div>
	{/if}
</header>
