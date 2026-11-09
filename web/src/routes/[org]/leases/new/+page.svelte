<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../../lib/api';
	import { session } from '../../../../lib/session.svelte';
	import { useUnsavedChanges } from '../../../../lib/forms/unsaved.svelte';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import LoadState from '../../../../lib/components/LoadState.svelte';
	import LeaseForm, { type NewLease } from '../../../../lib/modules/property/LeaseForm.svelte';
	const org = $derived(page.params.org ?? '');
	const allowed = $derived(session.currentOrg?.slug === org && session.currentOrg.perms.includes('property_write') && session.currentOrg.perms.includes('property_read'));
	let properties = $state<{ id: number; name: string }[]>([]);
	let tenants = $state<{ id: number; name: string; kind: string }[]>([]);
	let loaded = $state('');
	let error = $state('');
	let saveError = $state('');
	let busy = $state(false);
	let dirty = $state(false);
	let saved = $state<number | null>(null);
	let warnings = $state<string[]>([]);
	let seq = 0;
	const { allowLeave } = useUnsavedChanges(() => dirty, () => busy);
	async function load() {
		const slug = org;
		const token = ++seq;
		error = '';
		try {
			const [p, t] = await Promise.all([api<typeof properties>('GET', `/api/v1/orgs/${slug}/properties`), api<typeof tenants>('GET', `/api/v1/orgs/${slug}/contacts`)]);
			if (token !== seq || slug !== org || !allowed) return;
			properties = p; tenants = t.filter((c) => c.kind === 'tenant'); loaded = slug;
		} catch (e) { if (token === seq) error = e instanceof ApiError ? e.message : 'Could not load lease options.'; }
	}
	$effect(() => {
		org; allowed;
		untrack(() => { loaded = ''; saved = null; busy = false; dirty = false; warnings = []; if (allowed) load(); });
		return () => { seq++; };
	});
	async function save(body: NewLease) {
		if (!allowed || busy || loaded !== org || saved !== null) return;
		const slug = org;
		const token = seq;
		busy = true; saveError = '';
		try {
			const result = await api<{ id: number; warnings?: string[] }>('POST', `/api/v1/orgs/${slug}/leases`, body);
			if (token !== seq || slug !== org || !allowed) return;
			saved = result.id; warnings = result.warnings ?? []; dirty = false;
		} catch (e) { if (token === seq) saveError = e instanceof ApiError ? e.message : 'Could not create lease.'; }
		finally { if (token === seq) busy = false; }
	}
</script>

<PageHeader title="New lease" back={{ href: `/${org}/properties`, label: 'Back to properties' }} />
{#if !allowed}<p role="alert">You do not have permission to create leases.</p>
{:else}
	<LoadState loading={loaded !== org} {error} loadingLabel="Loading lease options…" onretry={load}>
		{#if saved !== null}
			<p role="status" class="alert alert-success alert-soft">Lease created. <a class="link" href={`/${org}/leases/${saved}`}>View saved lease</a></p>
			{#each warnings as warning}<p role="status" class="alert alert-warning alert-soft mt-3">{warning}</p>{/each}
		{:else if !properties.length || !tenants.length}<p role="status">Add a property and a tenant contact before creating a lease.</p>
		{:else}
			{#key org}<LeaseForm {properties} {tenants} {busy} error={saveError} onsubmit={save} onchange={() => dirty = true} oncancel={() => { if (allowLeave()) { dirty = false; goto(`/${org}/properties`); } }} />{/key}
		{/if}
	</LoadState>
{/if}
