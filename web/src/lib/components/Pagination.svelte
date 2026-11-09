<script lang="ts">
	import Button from './Button.svelte';
	let { offset, pageSize, total, count, onchange }: {
		offset: number; pageSize: number; total: number; count: number;
		onchange: (offset: number) => void;
	} = $props();
</script>

<nav aria-label="Pagination" class="mt-4 flex flex-wrap items-center justify-between gap-3">
	<p class="text-sm text-base-content/70">{count ? `${offset + 1}–${Math.min(offset + count, total)}` : '0'} of {total}</p>
	<div class="flex gap-2">
		<Button variant="secondary" disabled={offset <= 0} onclick={() => onchange(Math.max(0, offset - pageSize))}>Previous</Button>
		<Button variant="secondary" disabled={offset + pageSize >= total} onclick={() => onchange(offset + pageSize)}>Next</Button>
	</div>
</nav>
