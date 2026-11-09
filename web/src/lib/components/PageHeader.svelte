<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		title,
		back,
		heading,
		actions
	}: {
		title: string;
		back?: { href: string; label: string };
		heading?: Snippet;
		actions?: Snippet;
	} = $props();
</script>

<header class="mb-6 flex flex-wrap items-center justify-between gap-4">
	<div class="min-w-0">
		{#if back}<a class="link link-primary mb-2 inline-block text-sm" href={back.href}>{back.label}</a>{/if}
		{#if heading}{@render heading()}{:else}<h1 class="m-0 text-2xl font-bold">{title}</h1>{/if}
	</div>
	{#if actions}
		<div class="flex flex-wrap gap-2">{@render actions()}</div>
	{/if}
</header>
