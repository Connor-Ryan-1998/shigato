<script lang="ts">
	// InsuranceDialog adds a policy to a property, or edits/deletes one when
	// `policy` is set (a full PUT: every field is sent). A document is
	// uploaded on selection and attached by the save.
	import { untrack } from 'svelte';
	import { api, ApiError } from '../../api';
	import { parseCents } from '../../format';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';
	import { centsInput, INSURANCE_TYPES, type InsurancePolicy, type UploadedFile } from './types';

	let {
		open = $bindable(false),
		org,
		propertyId,
		policy = null,
		onsaved
	}: {
		open?: boolean;
		org: string;
		propertyId: number;
		policy?: InsurancePolicy | null;
		onsaved: () => void;
	} = $props();

	let insurer = $state('');
	let type = $state('landlord');
	let policyNumber = $state('');
	let start = $state('');
	let renewal = $state('');
	let sumInsured = $state('');
	let premium = $state('');
	let excess = $state('');
	let file = $state<UploadedFile | null>(null);
	let error = $state('');
	let busy = $state(false);
	let seq = 0;
	$effect(() => { org; propertyId; open; return () => { seq++; }; });

	$effect(() => {
		if (open) untrack(seed);
	});
	function seed() {
		error = ''; busy = false;
		insurer = policy?.insurer ?? '';
		type = policy?.type ?? 'landlord';
		policyNumber = policy?.policy_number ?? '';
		start = policy?.start_date ?? '';
		renewal = policy?.renewal_date ?? '';
		sumInsured = centsInput(policy?.sum_insured);
		premium = centsInput(policy?.premium);
		excess = centsInput(policy?.excess);
		file = policy?.file_id ? { id: policy.file_id, original_name: policy.file_name ?? 'Document' } : null;
	}

	const base = $derived(`/api/v1/orgs/${org}/properties/${propertyId}`);

	// optionalCents parses an optional dollar field: '' is null, and an
	// unparseable value is undefined.
	function optionalCents(v: string): number | null | undefined {
		if (v.trim() === '') return null;
		const c = parseCents(v);
		return c === null || c < 0 ? undefined : c;
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
			if (token === seq) file = saved;
		} catch (e) {
			if (token === seq) error = e instanceof ApiError ? e.message : 'Could not upload the document.';
		} finally {
			if (token === seq) { busy = false; input.value = ''; }
		}
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (busy || !open) return;
		const amounts = { sum_insured: optionalCents(sumInsured), premium: optionalCents(premium), excess: optionalCents(excess) };
		if (Object.values(amounts).some((v) => v === undefined)) {
			error = 'Enter amounts in dollars, with up to two decimal places.';
			return;
		}
		if (start && renewal && renewal < start) {
			error = 'Renewal date must not be before the start date.';
			return;
		}
		const body = { insurer, type, policy_number: policyNumber, start_date: start, renewal_date: renewal, ...amounts, file_id: file?.id ?? null };
		const token = seq;
		busy = true; error = '';
		try {
			if (policy) await api('PUT', `${base}/insurance/${policy.id}`, body);
			else await api('POST', `${base}/insurance`, body);
			if (token !== seq || !open) return;
			open = false;
			onsaved();
		} catch (err) {
			if (token === seq) error = err instanceof ApiError ? err.message : 'Could not save the policy.';
		} finally {
			if (token === seq) busy = false;
		}
	}

	async function remove() {
		if (!policy || busy || !window.confirm('Delete this policy?')) return;
		const token = seq;
		busy = true; error = '';
		try {
			await api('DELETE', `${base}/insurance/${policy.id}`);
			if (token !== seq) return;
			open = false;
			onsaved();
		} catch (err) {
			if (token === seq) error = err instanceof ApiError ? err.message : 'Could not delete the policy.';
		} finally {
			if (token === seq) busy = false;
		}
	}
</script>

<Dialog bind:open title={policy ? 'Edit insurance policy' : 'New insurance policy'} {busy}>
	{#snippet children({ close })}
	{#if open}
		<form class="flex flex-col gap-3" onsubmit={submit}>
			{#if error}<p class="alert alert-error alert-soft" role="alert">{error}</p>{/if}
			<fieldset disabled={busy} class="grid gap-3 sm:grid-cols-2">
				<Field label="Insurer" id="ins-insurer"><input class="input w-full" id="ins-insurer" bind:value={insurer} required /></Field>
				<Field label="Policy type" id="ins-type">
					<select class="select w-full" id="ins-type" bind:value={type}>
						{#each INSURANCE_TYPES as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
					</select>
				</Field>
				<Field label="Policy number" id="ins-number"><input class="input w-full" id="ins-number" bind:value={policyNumber} /></Field>
				<div></div>
				<Field label="Start date" id="ins-start"><input class="input w-full" id="ins-start" type="date" bind:value={start} required /></Field>
				<Field label="Renewal date" id="ins-renewal"><input class="input w-full" id="ins-renewal" type="date" bind:value={renewal} required /></Field>
				<Field label="Sum insured ($)" id="ins-sum"><input class="input w-full" id="ins-sum" inputmode="decimal" bind:value={sumInsured} /></Field>
				<Field label="Premium ($)" id="ins-premium"><input class="input w-full" id="ins-premium" inputmode="decimal" bind:value={premium} /></Field>
				<Field label="Excess ($)" id="ins-excess"><input class="input w-full" id="ins-excess" inputmode="decimal" bind:value={excess} /></Field>
				<Field label="Policy document" id="ins-file" hint={file ? `Attached: ${file.original_name}` : 'PDF, JPEG or PNG, up to 10 MB.'}>
					<input class="file-input w-full" id="ins-file" type="file" accept="application/pdf,image/jpeg,image/png" onchange={upload} />
				</Field>
			</fieldset>
			{#if file}<Button variant="secondary" size="sm" disabled={busy} onclick={() => (file = null)}>Remove document</Button>{/if}
			<div class="modal-action">
				{#if policy}<Button variant="danger" disabled={busy} onclick={remove}>Delete policy</Button>{/if}
				<Button variant="secondary" disabled={busy} onclick={close}>Cancel</Button>
				<Button type="submit" {busy}>{busy ? 'Saving…' : 'Save policy'}</Button>
			</div>
		</form>
	{/if}
	{/snippet}
</Dialog>
