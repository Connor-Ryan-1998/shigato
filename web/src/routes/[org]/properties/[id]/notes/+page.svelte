<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../../lib/api';
	import { session } from '../../../../../lib/session.svelte';
	import { useUnsavedChanges } from '../../../../../lib/forms/unsaved.svelte';
	import Field from '../../../../../lib/components/Field.svelte';
	import FormActions from '../../../../../lib/components/FormActions.svelte';
	import LoadState from '../../../../../lib/components/LoadState.svelte';

	const org = $derived(page.params.org ?? '');
	const id = $derived(page.params.id ?? '');
	const scope = $derived(`${org}/${id}`);
	const member = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(member?.perms.includes('property_read') ?? false);
	const canWrite = $derived(member?.perms.includes('property_write') ?? false);
	const base = $derived(`/api/v1/orgs/${org}/properties/${id}`);

	let notes = $state('');
	let saved = $state('');
	let loaded = $state('');
	let error = $state('');
	let saveError = $state('');
	let success = $state('');
	let saving = $state(false);
	let seq = 0;

	const dirty = $derived(loaded === scope && notes !== saved);
	const { allowLeave } = useUnsavedChanges(() => dirty, () => saving);

	async function load() {
		const token = ++seq;
		const s = scope;
		error = '';
		try {
			const p = await api<{ notes: string }>('GET', base);
			if (token !== seq || s !== scope || !canRead) return;
			notes = saved = p.notes ?? '';
			loaded = s;
		} catch (e) {
			if (token === seq && s === scope) error = e instanceof ApiError ? e.message : 'Could not load notes.';
		}
	}
	$effect(() => {
		scope; canRead;
		untrack(() => { loaded = ''; notes = saved = ''; saving = false; saveError = ''; success = ''; if (canRead) load(); });
		return () => { seq++; };
	});

	function cancel() {
		if (!allowLeave()) return;
		notes = saved;
		saveError = ''; success = '';
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (saving || !canWrite || loaded !== scope) return;
		const token = seq;
		const s = scope;
		const text = notes;
		saving = true; saveError = ''; success = '';
		try {
			const result = await api<{ notes: string }>('PUT', `${base}/notes`, { notes: text });
			if (token !== seq || s !== scope) return;
			saved = result.notes;
			if (notes === text) notes = result.notes;
			success = 'Notes saved.';
		} catch (err) {
			if (token === seq && s === scope) saveError = err instanceof ApiError ? err.message : 'Could not save notes.';
		} finally {
			if (token === seq) saving = false;
		}
	}
</script>

{#if !canRead}
	<p role="alert">You do not have permission to view properties.</p>
{:else}
	<LoadState loading={loaded !== scope} {error} loadingLabel="Loading notes…" onretry={load}>
		<form onsubmit={save} class="grid max-w-3xl gap-3">
			{#if saveError}<p role="alert" class="alert alert-error alert-soft">{saveError}</p>{/if}
			{#if success}<p role="status" class="alert alert-success alert-soft">{success}</p>{/if}
			<Field label="Notes" id="property-notes" hint="Anything worth remembering about this property: access, appliances, tradespeople, history.">
				<textarea class="textarea min-h-80 w-full" id="property-notes" bind:value={notes} disabled={saving || !canWrite}></textarea>
			</Field>
			{#if canWrite}<FormActions busy={saving} submitLabel="Save notes" oncancel={cancel} />{/if}
		</form>
	</LoadState>
{/if}
