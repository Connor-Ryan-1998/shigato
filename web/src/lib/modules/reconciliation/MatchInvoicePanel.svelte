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

<div class="flex flex-col gap-3">
	<Field label="Search open invoices" id="match-invoice-search">
		<input class="input w-full" id="match-invoice-search" bind:value={search} placeholder="Contact or invoice number" />
	</Field>

	{#if loadError}
		<p class="text-error" role="alert">{loadError}</p>
	{:else if filtered.length === 0}
		<p class="text-base-content/70">No open invoices match.</p>
	{:else}
		<ul class="m-0 flex list-none flex-col gap-2 p-0">
			{#each filtered as inv (inv.id)}
				<li class="flex items-center justify-between gap-3 rounded-box border border-base-300 px-3 py-2">
					<span class="text-sm">
						{inv.number !== null ? `#${inv.number}` : 'Draft'} — {inv.contact_name} —
						<Money cents={inv.outstanding} />
					</span>
					<Button disabled={busy} onclick={() => onMatch(inv.id)}>Match</Button>
				</li>
			{/each}
		</ul>
	{/if}
</div>
