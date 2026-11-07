<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import EmptyState from '../../../../lib/components/EmptyState.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import Money from '../../../../lib/components/Money.svelte';
	import DateText from '../../../../lib/components/DateText.svelte';

	type LeasePeriod = { start: string; end: string; amount_due: number };
	type Lease = {
		id: number;
		property_name: string;
		property_address: string;
		rent_amount: number;
		frequency: string;
		next_due?: LeasePeriod;
		arrears: number;
		invoiced_not_due: number;
	};

	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let errorMessage = $state('');
	let leases = $state<Lease[]>([]);

	// Bumped on every load() call; a response is only applied if it's still
	// the most recent request by the time it resolves - same pattern as
	// [org]/settings/members/+page.svelte.
	let requestSeq = 0;

	async function load() {
		const seq = ++requestSeq;
		loadStatus = 'loading';
		errorMessage = '';
		try {
			const org = page.params.org;
			// A client gets a 404 here (leases are tenant-only); routing to
			// this page is itself staff-nav-free, so a client never has a
			// link to it, but a typed URL still has to fail cleanly.
			const result = await api<Lease[]>('GET', `/api/v1/portal/${org}/leases`);
			if (seq !== requestSeq) return;
			leases = result;
			loadStatus = 'ready';
		} catch (e) {
			if (seq !== requestSeq) return;
			errorMessage = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	$effect(() => {
		page.params.org;
		load();
	});
</script>

<PageHeader title="Leases" />

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading leases…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load leases" description={errorMessage}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if leases.length === 0}
	<EmptyState title="No leases yet" />
{:else}
	<ul class="lease-list">
		{#each leases as lease (lease.id)}
			<li class="lease-card">
				<p class="address">{lease.property_address}</p>
				<p class="rent"><Money cents={lease.rent_amount} /> {lease.frequency}</p>
				{#if lease.next_due}
					<p class="next-due">
						Next due <DateText iso={lease.next_due.start} />&ndash;<DateText
							iso={lease.next_due.end}
						/>: <Money cents={lease.next_due.amount_due} />
					</p>
				{/if}
				{#if lease.arrears > 0}
					<p class="arrears">You're <Money cents={lease.arrears} /> behind</p>
				{/if}
			</li>
		{/each}
	</ul>
{/if}

<style>
	.lease-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
	.lease-card {
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
		padding: var(--space-4);
	}
	.address {
		font-weight: 600;
		margin: 0 0 var(--space-2);
	}
	.rent,
	.next-due {
		margin: 0 0 var(--space-1);
		color: var(--text-muted);
	}
	.arrears {
		margin: var(--space-2) 0 0;
		color: var(--danger);
		font-weight: 600;
	}
</style>
