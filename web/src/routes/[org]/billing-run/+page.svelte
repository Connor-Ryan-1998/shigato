<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import { session } from '../../../lib/session.svelte';
	import { todayLocal } from '../../../lib/format';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';

	type Contact = { id: number; name: string; kind: string };
	type RunResult = { invoice: { id: number }; entry_count: number; total_minutes: number };

	const today = todayLocal();
	let contacts = $state<Contact[]>([]);
	let contactId = $state('');
	let from = $state(`${today.slice(0, 7)}-01`);
	let to = $state(today);
	let dueInDays = $state('14');
	let issue = $state(false);
	let error = $state('');
	let running = $state(false);
	let loadedOrg = $state('');
	let invoiceID = $state<number | undefined>();
	let requestSeq = 0;

	const org = $derived(page.params.org);
	const canRun = $derived(session.currentOrg?.slug === org && (session.currentOrg?.perms.includes('invoices_write') ?? false));
	const options = $derived(contacts.map((c) => ({ value: String(c.id), label: c.name })));

	function message(e: unknown): string {
		return e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	async function load(slug: string) {
		const seq = ++requestSeq;
		loadedOrg = '';
		contacts = [];
		contactId = '';
		invoiceID = undefined;
		error = '';
		running = false;
		from = `${todayLocal().slice(0, 7)}-01`;
		to = todayLocal();
		dueInDays = '14';
		issue = false;
		try {
			const result = await api<Contact[]>('GET', `/api/v1/orgs/${slug}/contacts`);
			if (seq !== requestSeq || slug !== org || !canRun) return;
			contacts = result.filter(
				(c) => c.kind === 'client'
			);
			if (contacts[0]) contactId = String(contacts[0].id);
			loadedOrg = slug;
		} catch (e) {
			if (seq === requestSeq && slug === org && canRun) error = message(e);
		}
	}
	$effect(() => {
		if (canRun && org) load(org);
		return () => { requestSeq++; };
	});

	async function run(e: SubmitEvent) {
		e.preventDefault();
		if (!canRun || loadedOrg !== org || running || !contactId) return;
		if (!from || !to || from > to) { error = 'From must not be after To.'; return; }
		const days = Number(dueInDays);
		if (!Number.isInteger(days) || days < 0 || days > 90) { error = 'Due in days must be between 0 and 90.'; return; }
		const slug = org;
		const seq = requestSeq;
		running = true;
		error = '';
		invoiceID = undefined;
		try {
			const res = await api<RunResult>('POST', `/api/v1/orgs/${slug}/billing-runs`, {
				client_contact_id: Number(contactId),
				from,
				to,
				due_in_days: days,
				issue
			});
			if (seq !== requestSeq || slug !== org || !canRun) return;
			await goto(`/${slug}/invoices/${res.invoice.id}`);
		} catch (err) {
			if (seq !== requestSeq || slug !== org || !canRun) return;
			error = message(err);
			if (err instanceof ApiError) invoiceID = err.invoiceID;
		} finally {
			if (seq === requestSeq) running = false;
		}
	}
</script>

<PageHeader title="Billing run" />

{#if !canRun}
	<p role="alert">You do not have permission to create invoices.</p>
{:else if loadedOrg !== org}
	{#if !error}<p role="status">Loading clients…</p>{/if}
	{#if error}<Button onclick={() => org && load(org)}>Retry</Button>{/if}
{:else}
<p>Bill a client's unbilled time entries in a date range as one invoice.</p>

<form onsubmit={run} class="mt-4 grid max-w-sm gap-4">
	<fieldset disabled={running} class="fieldset grid gap-3 rounded-box border border-base-300 bg-base-100 p-4">
	<legend class="fieldset-legend">Billing period</legend>
	<Field label="Client" id="run-client">
		<Select id="run-client" bind:value={contactId} {options} />
	</Field>
	<Field label="From" id="run-from"><input class="input w-full" id="run-from" type="date" bind:value={from} required /></Field>
	<Field label="To" id="run-to"><input class="input w-full" id="run-to" type="date" bind:value={to} required /></Field>
	<Field label="Due in days" id="run-due">
		<input class="input w-full" id="run-due" type="number" min="0" max="90" step="1" bind:value={dueInDays} required />
	</Field>
	<label class="label"><input class="checkbox" type="checkbox" bind:checked={issue} /> Issue immediately</label>
	</fieldset>
	<Button type="submit" disabled={running || contactId === ''}>
		{running ? 'Running…' : 'Create invoice'}
	</Button>
</form>
{#if invoiceID}
	<p role="status" class="alert alert-warning alert-soft mt-4">Time entries are already billed to an existing invoice. Open it to review or retry issuing; do not create another billing run for these entries.</p>
	<a class="link link-primary" href={`/${org}/invoices/${invoiceID}`}>Open existing invoice</a>
{/if}
{/if}

{#if error}
	<p role="alert" class="alert alert-error alert-soft mt-4">{error}</p>
{/if}
