<script lang="ts">
	import type { Snippet } from 'svelte';
	let { label = 'Filters', children }: { label?: string; children: Snippet } = $props();
</script>

<section aria-label={label} class="mb-4 flex flex-wrap items-end gap-3 rounded-box border border-base-300 bg-base-100 p-3 [&_.fieldset]:min-w-0 [&_.fieldset]:flex-[1_1_10rem] [&_input]:max-w-full [&_select]:max-w-full">
	{@render children()}
</section>
