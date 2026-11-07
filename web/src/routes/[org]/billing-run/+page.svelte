<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, ApiError } from '../../../lib/api';
	import { todayLocal } from '../../../lib/format';
	import PageHeader from '../../../lib/components/PageHeader.svelte';
	import Field from '../../../lib/components/Field.svelte';
	import Select from '../../../lib/components/Select.svelte';
	import Button from '../../../lib/components/Button.svelte';
	import Toast from '../../../lib/components/Toast.svelte';

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

	const org = $derived(page.params.org);
	const options = $derived(contacts.map((c) => ({ value: String(c.id), label: c.name })));

	function message(e: unknown): string {
		return e instanceof ApiError ? e.message : 'Something went wrong.';
	}

	onMount(async () => {
		try {
			contacts = (await api<Contact[]>('GET', `/api/v1/orgs/${org}/contacts`)).filter(
				(c) => c.kind === 'client'
			);
			if (contacts[0]) contactId = String(contacts[0].id);
		} catch (e) {
			error = message(e);
		}
	});

	async function run(e: SubmitEvent) {
		e.preventDefault();
		running = true;
		error = '';
		try {
			const res = await api<RunResult>('POST', `/api/v1/orgs/${org}/billing-runs`, {
				client_contact_id: Number(contactId),
				from,
				to,
				due_in_days: Number(dueInDays),
				issue
			});
			await goto(`/${org}/invoices/${res.invoice.id}`);
		} catch (err) {
			error = message(err);
		} finally {
			running = false;
		}
	}
</script>

<PageHeader title="Billing run" />

<p>Bill a client's unbilled time entries in a date range as one invoice.</p>

<form onsubmit={run}>
	<Field label="Client" id="run-client">
		<Select id="run-client" bind:value={contactId} {options} />
	</Field>
	<Field label="From" id="run-from"><input id="run-from" type="date" bind:value={from} required /></Field>
	<Field label="To" id="run-to"><input id="run-to" type="date" bind:value={to} required /></Field>
	<Field label="Due in days" id="run-due">
		<input id="run-due" type="number" min="0" bind:value={dueInDays} required />
	</Field>
	<label class="check"><input type="checkbox" bind:checked={issue} /> Issue immediately</label>
	<Button type="submit" disabled={running || contactId === ''}>
		{running ? 'Running…' : 'Create invoice'}
	</Button>
</form>

{#if error}
	<Toast message={error} tone="danger" onDismiss={() => (error = '')} />
{/if}

<style>
	form {
		display: grid;
		gap: var(--space-4, 1rem);
		max-width: 24rem;
	}
	.check {
		display: flex;
		gap: var(--space-2, 0.5rem);
		align-items: center;
	}
</style>
