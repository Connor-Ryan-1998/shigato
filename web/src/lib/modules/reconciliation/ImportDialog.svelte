<script lang="ts">
	import { api, ApiError } from '../../api';
	import Dialog from '../../components/Dialog.svelte';
	import Field from '../../components/Field.svelte';
	import Button from '../../components/Button.svelte';

	let {
		open = $bindable(false),
		org,
		accountId,
		accountName
	}: {
		open?: boolean;
		org: string;
		accountId: number;
		accountName: string;
	} = $props();

	let file = $state<File | null>(null);
	let busy = $state(false);
	// error carries the server's message verbatim - including a 413 "file
	// too large" and a 422 row-numbered CSV error - exactly as the API
	// contract requires.
	let error = $state('');
	let result = $state<{ rows_total: number; rows_new: number } | null>(null);

	$effect(() => {
		if (!open) return;
		file = null;
		error = '';
		result = null;
	});

	function pickFile(e: Event) {
		file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null;
	}

	async function submit() {
		if (!file || busy) return;
		busy = true;
		error = '';
		try {
			const body = new FormData();
			body.append('file', file);
			result = await api<{ rows_total: number; rows_new: number }>(
				'POST',
				`/api/v1/orgs/${org}/bank-accounts/${accountId}/imports`,
				body
			);
		} catch (e) {
			error = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			busy = false;
		}
	}
</script>

<Dialog bind:open title={`Import CSV — ${accountName}`}>
	{#if result}
		<p class="mb-4">
			Imported {result.rows_new} new of {result.rows_total} rows.
		</p>
		<div class="flex justify-end gap-3">
			<Button onclick={() => (open = false)}>Done</Button>
		</div>
	{:else}
		<Field label="CSV file" id="import-file">
			<!-- Keyed by `open`: a native file input keeps showing its last-
			     picked filename even after `file` resets to null above, so a
			     dialog reopened after a previous import still displayed the
			     old filename without this remount. -->
			{#key open}
				<input class="file-input w-full" id="import-file" type="file" accept=".csv,text/csv" onchange={pickFile} />
			{/key}
		</Field>
		{#if error}
			<p class="alert alert-error alert-soft my-3 whitespace-pre-wrap" role="alert">{error}</p>
		{/if}
		<div class="flex justify-end gap-3">
			<Button variant="secondary" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
			<Button disabled={busy || !file} onclick={submit}>Import</Button>
		</div>
	{/if}
</Dialog>
