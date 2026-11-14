<script lang="ts">
	// DepreciationItemDialog adds an item to a property's depreciation
	// schedule, or edits/deletes one when `item` is set (a full PUT that
	// replaces its yearly amounts and documents). Amounts are typed in from
	// the surveyor's report; nothing is calculated here.
	import { untrack } from 'svelte';
	import { api, ApiError } from '../../api';
	import { parseCents } from '../../format';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';
	import { centsInput, DEPRECIATION_TYPES, type DepreciationItem, type UploadedFile } from './types';
	import { fyLabel } from './fy';

	let {
		open = $bindable(false),
		org,
		propertyId,
		item = null,
		defaultFY,
		onsaved
	}: {
		open?: boolean;
		org: string;
		propertyId: number;
		item?: DepreciationItem | null;
		defaultFY: number;
		onsaved: () => void;
	} = $props();

	type Row = { key: number; fy: string; amount: string };

	let name = $state('');
	let type = $state('capital_works');
	let installedOn = $state('');
	let cost = $state('');
	let notes = $state('');
	let rows = $state<Row[]>([]);
	let documents = $state<UploadedFile[]>([]);
	let error = $state('');
	let busy = $state(false);
	let nextKey = 0;
	let seq = 0;
	$effect(() => { org; propertyId; open; return () => { seq++; }; });

	$effect(() => {
		if (open) untrack(seed);
	});
	function seed() {
		error = ''; busy = false;
		name = item?.name ?? '';
		type = item?.type ?? 'capital_works';
		installedOn = item?.installed_on ?? '';
		cost = centsInput(item?.cost);
		notes = item?.notes ?? '';
		rows = (item?.amounts ?? []).map((a) => ({ key: nextKey++, fy: String(a.fy), amount: centsInput(a.amount) }));
		documents = (item?.documents ?? []).map((d) => ({ ...d }));
	}

	const years = $derived(Array.from({ length: 81 }, (_, i) => defaultFY + 40 - i));
	const base = $derived(`/api/v1/orgs/${org}/properties/${propertyId}`);

	function addYear() {
		const last = rows.at(-1);
		rows.push({ key: nextKey++, fy: String(last ? Number(last.fy) + 1 : defaultFY), amount: '' });
	}

	async function upload(event: Event) {
		const input = event.currentTarget as HTMLInputElement;
		const chosen = input.files?.[0];
		if (!chosen || busy) return;
		const token = seq;
		busy = true; error = '';
		try {
			const body = new FormData(); body.append('file', chosen);
			const saved = await api<UploadedFile>('POST', `${base}/files`, body);
			if (token === seq && !documents.some((d) => d.id === saved.id)) documents.push(saved);
		} catch (e) {
			if (token === seq) error = e instanceof ApiError ? e.message : 'Could not upload the document.';
		} finally {
			if (token === seq) { busy = false; input.value = ''; }
		}
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (busy || !open) return;
		const costCents = cost.trim() === '' ? null : parseCents(cost);
		if (cost.trim() !== '' && (costCents === null || costCents < 0)) {
			error = 'Enter the cost in dollars, with up to two decimal places.';
			return;
		}
		const amounts = rows.map((r) => ({ fy: Number(r.fy), amount: parseCents(r.amount) }));
		if (amounts.some((a) => a.amount === null || a.amount <= 0)) {
			error = 'Each yearly amount must be a positive dollar amount.';
			return;
		}
		if (new Set(amounts.map((a) => a.fy)).size !== amounts.length) {
			error = 'Each financial year can appear only once.';
			return;
		}
		const body = {
			name, type, installed_on: installedOn, cost: costCents, notes, expense_id: item?.expense_id ?? null,
			amounts: amounts.sort((a, b) => a.fy - b.fy), document_ids: documents.map((d) => d.id)
		};
		const token = seq;
		busy = true; error = '';
		try {
			if (item) await api('PUT', `${base}/depreciation/${item.id}`, body);
			else await api('POST', `${base}/depreciation`, body);
			if (token !== seq || !open) return;
			open = false;
			onsaved();
		} catch (err) {
			if (token === seq) error = err instanceof ApiError ? err.message : 'Could not save the item.';
		} finally {
			if (token === seq) busy = false;
		}
	}

	async function remove() {
		if (!item || busy || !window.confirm('Delete this depreciation item?')) return;
		const token = seq;
		busy = true; error = '';
		try {
			await api('DELETE', `${base}/depreciation/${item.id}`);
			if (token !== seq) return;
			open = false;
			onsaved();
		} catch (err) {
			if (token === seq) error = err instanceof ApiError ? err.message : 'Could not delete the item.';
		} finally {
			if (token === seq) busy = false;
		}
	}
</script>

<Dialog bind:open title={item ? 'Edit depreciation item' : 'New depreciation item'} {busy}>
	{#snippet children({ close })}
	{#if open}
		<form class="flex flex-col gap-3" onsubmit={submit}>
			{#if error}<p class="alert alert-error alert-soft" role="alert">{error}</p>{/if}
			<fieldset disabled={busy} class="grid gap-3 sm:grid-cols-2">
				<Field label="Name" id="dep-name"><input class="input w-full" id="dep-name" bind:value={name} required placeholder="Kitchen renovation" /></Field>
				<Field label="Type" id="dep-type">
					<select class="select w-full" id="dep-type" bind:value={type}>
						{#each DEPRECIATION_TYPES as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
					</select>
				</Field>
				<Field label="Installed on" id="dep-installed"><input class="input w-full" id="dep-installed" type="date" bind:value={installedOn} required /></Field>
				<Field label="Cost ($)" id="dep-cost"><input class="input w-full" id="dep-cost" inputmode="decimal" bind:value={cost} /></Field>
				<div class="sm:col-span-2">
					<Field label="Notes" id="dep-notes"><textarea class="textarea w-full" id="dep-notes" bind:value={notes}></textarea></Field>
				</div>
			</fieldset>

			<fieldset disabled={busy} class="fieldset rounded-box border border-base-300 p-3">
				<legend class="fieldset-legend">Yearly deductions</legend>
				{#each rows as row, i (row.key)}
					<div class="flex flex-wrap items-end gap-2">
						<Field label={`Year ${i + 1}`} id={`dep-fy-${row.key}`}>
							<select class="select" id={`dep-fy-${row.key}`} bind:value={row.fy}>
								{#each years as y (y)}<option value={String(y)}>{fyLabel(y)}</option>{/each}
							</select>
						</Field>
						<Field label={`Amount ${i + 1} ($)`} id={`dep-amount-${row.key}`}>
							<input class="input" id={`dep-amount-${row.key}`} inputmode="decimal" bind:value={row.amount} required />
						</Field>
						<Button variant="secondary" size="sm" onclick={() => rows.splice(i, 1)}>Remove year {i + 1}</Button>
					</div>
				{:else}
					<p class="text-base-content/70">No yearly amounts yet.</p>
				{/each}
				<div><Button variant="secondary" size="sm" onclick={addYear}>Add year</Button></div>
			</fieldset>

			<fieldset disabled={busy} class="fieldset rounded-box border border-base-300 p-3">
				<legend class="fieldset-legend">Documents</legend>
				{#each documents as d, i (d.id)}
					<div class="flex items-center gap-2">
						<span>{d.original_name}</span>
						<Button variant="secondary" size="sm" onclick={() => documents.splice(i, 1)}>Remove {d.original_name}</Button>
					</div>
				{/each}
				<Field label="Add document" id="dep-file" hint="Surveyor's report, invoice or photo: PDF, JPEG or PNG, up to 10 MB.">
					<input class="file-input w-full" id="dep-file" type="file" accept="application/pdf,image/jpeg,image/png" onchange={upload} />
				</Field>
			</fieldset>

			<div class="modal-action">
				{#if item}<Button variant="danger" disabled={busy} onclick={remove}>Delete item</Button>{/if}
				<Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button>
				<Button type="submit" {busy}>{busy ? 'Saving…' : 'Save item'}</Button>
			</div>
		</form>
	{/if}
	{/snippet}
</Dialog>
