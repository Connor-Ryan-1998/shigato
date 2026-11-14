<script lang="ts">
	// Item21MappingDialog sets which rental-schedule (Item 21) label each
	// expense category's lines land on. Only changed categories are saved,
	// each with the strict full category body the categories PUT expects.
	// Capital allowances and capital works aren't offered: only depreciation
	// items feed them.
	import { untrack } from 'svelte';
	import { api, ApiError } from '../../api';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';
	import LoadState from '../../components/LoadState.svelte';
	import { ITEM21_LABELS } from './types';

	type Category = { id: number; name: string; type: string; default_gst_code?: string; capital: boolean; item21_label: string | null };

	let { open = $bindable(false), org, onsaved }: { open?: boolean; org: string; onsaved: () => void } = $props();

	let categories = $state<Category[]>([]);
	let labels = $state<Record<number, string>>({});
	let loaded = $state(false);
	let loadError = $state('');
	let error = $state('');
	let busy = $state(false);
	let seq = 0;
	$effect(() => { org; open; return () => { seq++; }; });

	const options = [{ value: '', label: 'Not mapped (Sundry, with a warning)' }, ...ITEM21_LABELS.filter((l) => !l.depreciationOnly)];

	async function load() {
		const token = ++seq;
		loaded = false; loadError = ''; error = ''; busy = false;
		try {
			const all = await api<Category[]>('GET', `/api/v1/orgs/${org}/categories`);
			if (token !== seq) return;
			categories = all.filter((c) => c.type === 'expense');
			labels = Object.fromEntries(categories.map((c) => [c.id, c.item21_label ?? '']));
			loaded = true;
		} catch (e) {
			if (token === seq) loadError = e instanceof ApiError ? e.message : 'Could not load categories.';
		}
	}
	$effect(() => {
		if (open) untrack(load);
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (busy || !open || !loaded) return;
		const changed = categories.filter((c) => (c.item21_label ?? '') !== labels[c.id]);
		const token = seq;
		busy = true; error = '';
		try {
			for (const c of changed) {
				const label = labels[c.id] || null;
				await api('PUT', `/api/v1/orgs/${org}/categories/${c.id}`, {
					name: c.name, type: c.type, default_gst_code: c.default_gst_code ?? '', capital: c.capital, item21_label: label
				});
				if (token !== seq) return;
				c.item21_label = label;
			}
			open = false;
			onsaved();
		} catch (err) {
			if (token === seq) error = err instanceof ApiError ? err.message : 'Could not save labels.';
		} finally {
			if (token === seq) busy = false;
		}
	}
</script>

<Dialog bind:open title="Item 21 labels" {busy}>
	{#snippet children({ close })}
	{#if open}
		<LoadState loading={!loaded} error={loadError} loadingLabel="Loading categories…" onretry={load}>
			<form class="flex flex-col gap-3" onsubmit={submit}>
				<p class="text-base-content/70">Choose where each expense category appears on the ATO rental schedule.</p>
				{#if error}<p class="alert alert-error alert-soft" role="alert">{error}</p>{/if}
				<fieldset disabled={busy} class="grid gap-2">
					{#each categories as c (c.id)}
						<Field label={c.name} id={`item21-${c.id}`}>
							<select class="select w-full" id={`item21-${c.id}`} bind:value={labels[c.id]}>
								{#each options as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
							</select>
						</Field>
					{/each}
				</fieldset>
				<div class="modal-action">
					<Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button>
					<Button type="submit" {busy}>{busy ? 'Saving…' : 'Save labels'}</Button>
				</div>
			</form>
		</LoadState>
	{/if}
	{/snippet}
</Dialog>
