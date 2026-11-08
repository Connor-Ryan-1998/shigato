<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../lib/api';
	import EmptyState from '../../lib/components/EmptyState.svelte';
	import Button from '../../lib/components/Button.svelte';

	type PortalOrg = { slug: string; name: string; role: string };

	let status = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let orgs = $state<PortalOrg[]>([]);

	async function load() {
		status = 'loading';
		errorMessage = '';
		try {
			const result = await api<PortalOrg[]>('GET', '/api/v1/portal/orgs');
			// A single org skips the picker entirely - most portal users
			// belong to exactly one.
			if (result.length === 1) {
				goto(`/portal/${result[0].slug}/invoices`);
				return;
			}
			orgs = result;
			status = 'ready';
		} catch (e) {
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			status = 'error';
		}
	}

	onMount(load);
</script>

{#if status === 'loading'}
	<p aria-live="polite">Loading…</p>
{:else if status === 'error'}
	<EmptyState title="Couldn't load your organisations" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if orgs.length === 0}
	<EmptyState title="No organisations yet" />
{:else}
	<h1 class="mb-4 text-2xl font-bold">Choose an organisation</h1>
	<ul class="menu w-full max-w-md rounded-box bg-base-100 shadow-sm">
		{#each orgs as org (org.slug)}
			<li><a href={`/portal/${org.slug}/invoices`}>{org.name}</a></li>
		{/each}
	</ul>
{/if}
