<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ApiError } from '../../api';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';
	import Money from '../../components/Money.svelte';

	type OpenInvoice = {
		id: number;
		number: number | null;
		contact_name: string;
		outstanding: number;
	};

	let {
		org,
		busy = false,
		onMatch
	}: {
		org: string;
		busy?: boolean;
		onMatch: (invoiceId: number) => void;
	} = $props();

	let invoices = $state<OpenInvoice[]>([]);
	let loadError = $state('');
	let search = $state('');

	onMount(async () => {
		try {
			const res = await api<{ invoices: OpenInvoice[]; total: number }>(
				'GET',
				`/api/v1/orgs/${org}/invoices?status=open&limit=200`
			);
			invoices = res.invoices;
		} catch (e) {
			loadError = e instanceof ApiError ? e.message : 'Could not load open invoices.';
		}
	});

	const filtered = $derived(
		invoices.filter((inv) => {
			const q = search.trim().toLowerCase();
			if (q === '') return true;
			return (
				inv.contact_name.toLowerCase().includes(q) ||
				String(inv.number ?? '').includes(q)
			);
		})
	);
</script>

<div class="panel">
	<Field label="Search open invoices" id="match-invoice-search">
		<input id="match-invoice-search" bind:value={search} placeholder="Contact or invoice number" />
	</Field>

	{#if loadError}
		<p class="error" role="alert">{loadError}</p>
	{:else if filtered.length === 0}
		<p class="empty">No open invoices match.</p>
	{:else}
		<ul class="invoice-list">
			{#each filtered as inv (inv.id)}
				<li>
					<span class="invoice-info">
						{inv.number !== null ? `#${inv.number}` : 'Draft'} — {inv.contact_name} —
						<Money cents={inv.outstanding} />
					</span>
					<Button disabled={busy} onclick={() => onMatch(inv.id)}>Match</Button>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.panel {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}
	.invoice-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}
	.invoice-list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
	}
	.invoice-info {
		font-size: var(--text-sm);
	}
	.error {
		color: var(--danger);
	}
	.empty {
		color: var(--text-muted);
	}
</style>
