<script lang="ts">
	import type { Snippet } from 'svelte';
	import Button from './Button.svelte';
	import EmptyState from './EmptyState.svelte';
	let { loading, error = '', empty = false, loadingLabel, emptyTitle = 'No records yet', emptyDescription, onretry, children }: {
		loading: boolean; error?: string; empty?: boolean; loadingLabel: string;
		emptyTitle?: string; emptyDescription?: string; onretry?: () => void; children: Snippet;
	} = $props();
</script>

{#if error}
	<div class="mb-4 grid gap-3">
		<p role="alert" class="alert alert-error alert-soft">{error}</p>
		{#if onretry}<div><Button variant="secondary" onclick={onretry}>Retry</Button></div>{/if}
	</div>
{:else if loading}
	<p role="status" class="my-4 flex items-center gap-2"><span class="loading loading-spinner loading-sm" aria-hidden="true"></span>{loadingLabel}</p>
{:else if empty}
	<EmptyState title={emptyTitle} description={emptyDescription} />
{:else}
	{@render children()}
{/if}
