<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';

	type Settings = {
		name: string;
		abn: string;
		gst_registered: boolean;
		bas_frequency: string;
		bank_bsb: string;
		bank_account: string;
		payid: string;
		invoice_lead_days: number;
		auto_send_rent: boolean;
	};

	const org = $derived(page.params.org);
	const canManage = $derived(
		session.currentOrg?.slug === org && (session.currentOrg?.perms.includes('org_admin') ?? false)
	);
	let settings = $state<Settings | null>(null);
	let loadedOrg = $state('');
	let loading = $state(false);
	let saving = $state(false);
	let error = $state('');
	let success = $state('');
	let requestSeq = 0;

	async function load(slug: string) {
		const seq = ++requestSeq;
		settings = null;
		loadedOrg = '';
		error = '';
		success = '';
		saving = false;
		loading = true;
		try {
			const result = await api<Settings>('GET', `/api/v1/orgs/${slug}/settings`);
			if (seq !== requestSeq || slug !== org || !canManage) return;
			settings = result;
			loadedOrg = slug;
		} catch (e) {
			if (seq !== requestSeq || slug !== org || !canManage) return;
			error = e instanceof ApiError ? e.message : 'Could not load settings.';
		} finally {
			if (seq === requestSeq) loading = false;
		}
	}

	$effect(() => {
		if (canManage && org) load(org);
		return () => { requestSeq++; };
	});

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!canManage || !settings || loadedOrg !== org || saving) return;
		const slug = org;
		const seq = requestSeq;
		saving = true;
		error = '';
		success = '';
		try {
			// PUT replaces the whole record, including empty optional values.
			const result = await api<Settings>('PUT', `/api/v1/orgs/${slug}/settings`, { ...settings });
			if (seq !== requestSeq || slug !== org || !canManage) return;
			settings = result;
			success = 'Settings saved.';
			if (session.me) {
				session.setMe({ ...session.me, orgs: session.me.orgs.map((o) => o.slug === slug ? { ...o, name: result.name } : o) });
			}
		} catch (e) {
			if (seq !== requestSeq || slug !== org || !canManage) return;
			error = e instanceof ApiError ? e.message : 'Could not save settings.';
		} finally {
			if (seq === requestSeq) saving = false;
		}
	}
</script>

<PageHeader title="Organisation settings" />

{#if !canManage}
	<p role="alert">You do not have permission to manage settings.</p>
{:else}
	{#if error}<p role="alert" class="error">{error}</p>{/if}
	{#if success}<p role="status">{success}</p>{/if}
	{#if loading || (loadedOrg !== org && !error)}
		<p role="status">Loading settings…</p>
	{:else if settings && loadedOrg === org}
		<form onsubmit={save}>
			<fieldset disabled={saving}>
				<legend>Organisation and tax</legend>
				<Field label="Organisation name" id="settings-name">
					<input id="settings-name" bind:value={settings.name} required />
				</Field>
				<Field label="ABN" id="settings-abn">
					<input id="settings-abn" bind:value={settings.abn} />
				</Field>
				<label><input type="checkbox" bind:checked={settings.gst_registered} /> GST registered</label>
				<Field label="BAS frequency" id="settings-bas">
					<Select id="settings-bas" bind:value={settings.bas_frequency} options={[
						{ value: '', label: 'Not set' },
						{ value: 'quarterly', label: 'Quarterly' },
						{ value: 'monthly', label: 'Monthly' }
					]} />
				</Field>
			</fieldset>
			<fieldset disabled={saving}>
				<legend>Payment instructions</legend>
				<p>Clearing these instructions can remove payment options from rent and bond invoices.</p>
				<Field label="BSB" id="settings-bsb"><input id="settings-bsb" bind:value={settings.bank_bsb} /></Field>
				<Field label="Bank account" id="settings-account"><input id="settings-account" bind:value={settings.bank_account} /></Field>
				<Field label="PayID" id="settings-payid"><input id="settings-payid" bind:value={settings.payid} /></Field>
			</fieldset>
			<fieldset disabled={saving}>
				<legend>Rent invoicing</legend>
				<Field label="Invoice lead days" id="settings-lead">
					<input id="settings-lead" type="number" min="0" max="14" step="1" bind:value={settings.invoice_lead_days} required />
				</Field>
				<label><input type="checkbox" bind:checked={settings.auto_send_rent} /> Send rent invoices automatically</label>
				<p>Automatic sending uses the configured payment provider.</p>
			</fieldset>
			<Button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save settings'}</Button>
		</form>
	{:else}
		<Button onclick={() => org && load(org)}>Retry</Button>
	{/if}
{/if}

<style>
	form { display: grid; gap: var(--space-4); max-width: 40rem; }
	fieldset { display: grid; gap: var(--space-3); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-4); }
	legend { font-weight: 600; }
	p { margin: 0; }
	.error { color: var(--danger); }
</style>
